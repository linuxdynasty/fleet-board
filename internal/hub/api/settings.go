package api

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// settingsScopeKey renders a GetSettings scope as its settings_layers
// key: "fleet", "group/<label>" or "machine/<id>". A missing scope is
// INVALID_ARGUMENT on "scope"; a malformed group or machine name names
// its own field.
func settingsScopeKey(scope *hubv1.SettingsScope) (string, error) {
	var v violations
	switch {
	case scope.GetFleet() != nil:
		return statedb.FleetScope, nil
	case scope.GetGroupLabel() != "":
		label := scope.GetGroupLabel()
		if !labelName.MatchString(label) {
			v.add("scope.group_label", typev1.ReasonCode_REASON_CODE_INVALID_NAME,
				"a group is 1 to 32 characters of a-z, 0-9 and '-', starting with a letter or digit")
			return "", apiError(connect.CodeInvalidArgument, v[0].GetReason(),
				"the request has invalid fields", nil, v)
		}
		return "group/" + label, nil
	case scope.GetMachine() != nil:
		id := scope.GetMachine().GetValue()
		if !hubID.MatchString(id) {
			v.add("scope.machine.value", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"the machine id is 1 to 32 characters of a-z, 0-9 and '-', starting with a letter or digit")
			return "", apiError(connect.CodeInvalidArgument, v[0].GetReason(),
				"the request has invalid fields", nil, v)
		}
		return statedb.MachineScopeKey(id), nil
	default:
		v.add("scope", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"scope names the fleet, group or machine layer")
		return "", apiError(connect.CodeInvalidArgument, v[0].GetReason(),
			"the request has invalid fields", nil, v)
	}
}

// settingsLayerScope fills a stored layer's spec scope from its key.
// Machine layers store the scope unset (the scope column names the
// machine), so every served spec carries the layer's identity.
func settingsLayerScope(key string) *hubv1.SettingsScope {
	switch {
	case key == statedb.FleetScope:
		return &hubv1.SettingsScope{Kind: &hubv1.SettingsScope_Fleet_{Fleet: &hubv1.SettingsScope_Fleet{}}}
	case strings.HasPrefix(key, "group/"):
		return &hubv1.SettingsScope{Kind: &hubv1.SettingsScope_GroupLabel{GroupLabel: strings.TrimPrefix(key, "group/")}}
	default:
		return &hubv1.SettingsScope{Kind: &hubv1.SettingsScope_Machine{
			Machine: &typev1.MachineId{Value: strings.TrimPrefix(key, "machine/")},
		}}
	}
}

// settingsFromLayer answers the Settings of a stored layer with its
// decoded spec. Status carries the stamp of the write; delivery
// generations are computed by the daemon, so reads leave them unset.
func settingsFromLayer(stored statedb.SettingsLayerStored, spec *hubv1.SettingsSpec) *hubv1.Settings {
	out := settingsFromStored(statedb.SettingsStored{
		Version: stored.Version, CreateMs: stored.CreateMs,
		UpdateMs: stored.UpdateMs, HubSeq: stored.HubSeq, Spec: stored.Spec,
	}, spec)
	out.Id = &hubv1.SettingsId{Value: stored.Scope}
	return out
}

// settingsViewOf decodes one stored layer's spec and fills its scope.
// A row the hub itself wrote that no longer parses is the hub's data
// error.
func settingsViewOf(stored statedb.SettingsLayerStored) (settingsView, error) {
	var spec hubv1.SettingsSpec
	if err := protojson.Unmarshal(stored.Spec, &spec); err != nil {
		return settingsView{}, fmt.Errorf("api: settings spec: %w", err)
	}
	spec.Scope = settingsLayerScope(stored.Scope)
	return settingsView{stored: stored, spec: &spec}, nil
}

// GetSettings reads one settings layer by scope: the fleet defaults,
// one machine's layer, or a group layer once the group management
// row stores them. A scope nothing stored misses with
// RESOURCE_NOT_FOUND.
func (f *Fleet) GetSettings(ctx context.Context, req *connect.Request[hubv1.GetSettingsRequest],
) (*connect.Response[hubv1.GetSettingsResponse], error) {
	who, err := authorizeRead(ctx)
	if err != nil {
		return nil, f.settingsReadRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	key, err := settingsScopeKey(req.Msg.GetScope())
	if err != nil {
		return nil, f.settingsReadRefused(ctx, err)
	}
	stored, found, err := statedb.ReadSettingsLayer(ctx, f.store, key)
	if err != nil {
		return nil, f.settingsReadFailed(ctx, actor, "store", err)
	}
	if !found {
		return nil, f.settingsReadRefused(ctx, apiError(connect.CodeNotFound,
			typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND, "no settings layer has this scope", nil, nil))
	}
	v, err := settingsViewOf(stored)
	if err != nil {
		return nil, f.settingsReadFailed(ctx, actor, "store", err)
	}
	if err := quotaCheckpoint(ctx, quotaRead); err != nil {
		return nil, err
	}
	return connect.NewResponse(&hubv1.GetSettingsResponse{Settings: settingsFromLayer(v.stored, v.spec)}), nil
}

// ListSettings lists every stored settings layer (02k): the fleet
// defaults with the hub's own choices, and each machine's layer the
// import or the enroll stored. Filter and order_by follow the 02c
// Settings catalogue (settingsFilterFields names what this slice
// serves); page_size defaults to 50 and caps at 500, and the page
// token is bound to its query for 10 min.
func (f *Fleet) ListSettings(ctx context.Context, req *connect.Request[hubv1.ListSettingsRequest],
) (*connect.Response[hubv1.ListSettingsResponse], error) {
	who, err := authorizeRead(ctx)
	if err != nil {
		return nil, f.settingsReadRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	node, ferr := parseFilter(m.GetFilter(), settingsFilterFields)
	if ferr != nil {
		return nil, f.settingsReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_FILTER_INVALID, ferr.msg,
			map[string]string{"offset": fmt.Sprint(ferr.offset)},
			[]*typev1.FieldViolation{{Field: "filter", Description: ferr.msg}}))
	}
	order, oerr := parseOrderBy(m.GetOrderBy(), settingsOrderFields)
	if oerr != nil {
		return nil, f.settingsReadRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_ORDER_BY_INVALID, oerr.msg, nil,
			[]*typev1.FieldViolation{{Field: "order_by", Description: oerr.msg}}))
	}
	if order == nil {
		order = defaultSettingsOrder
	}
	size := int(m.GetPageSize())
	if size <= 0 {
		size = 50
	}
	if size > 500 {
		size = 500
	}
	now := f.hubNow()
	want := settingsQueryHash(m.GetFilter(), m.GetOrderBy(), size)
	var cursor settingsCursor
	if m.GetPageToken() != "" {
		cursor, err = parseSettingsToken(m.GetPageToken(), want, len(order), now)
		if err != nil {
			return nil, f.settingsReadRefused(ctx, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID, "bad page_token", nil,
				[]*typev1.FieldViolation{{Field: "page_token", Description: "bad page_token"}}))
		}
	}
	if err := quotaCheckpoint(ctx, quotaRead); err != nil {
		return nil, err
	}
	rows, err := statedb.ListSettingsLayers(ctx, f.store)
	if err != nil {
		return nil, f.settingsReadFailed(ctx, actor, "store", err)
	}
	views := make([]settingsView, 0, len(rows))
	for _, r := range rows {
		v, err := settingsViewOf(r)
		if err != nil {
			return nil, f.settingsReadFailed(ctx, actor, "store", err)
		}
		views = append(views, v)
	}
	matched := views[:0]
	for _, v := range views {
		if nodeMatchesSettings(node, v) {
			matched = append(matched, v)
		}
	}
	sortSettingsViews(matched, order)
	start := 0
	if m.GetPageToken() != "" {
		start = sort.Search(len(matched), func(i int) bool {
			return settingsKeyAfter(matched[i], order, cursor.key, cursor.id)
		})
	}
	end := min(start+size, len(matched))
	page := matched[start:end]
	out := make([]*hubv1.Settings, 0, len(page))
	for _, v := range page {
		out = append(out, settingsFromLayer(v.stored, v.spec))
	}
	next := ""
	if end < len(matched) {
		last := matched[end-1]
		next = mintSettingsToken(want, settingsSortKey(last, order), last.stored.Scope, now)
	}
	return connect.NewResponse(&hubv1.ListSettingsResponse{Settings: out, NextPageToken: next}), nil
}

// settingsReadRefused logs a settings-read caller error and returns
// it. The reads add no counter: only the writes count outcomes.
func (f *Fleet) settingsReadRefused(ctx context.Context, err error) error {
	f.tel.Logger.InfoContext(ctx, "settings.issue_refused", "reason", reasonOf(err))
	return err
}

// settingsReadFailed logs the hub's own settings-read failure and
// returns INTERNAL without the cause's text.
func (f *Fleet) settingsReadFailed(ctx context.Context, actor, step string, cause error) error {
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "settings.failed", "step", step, "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not read its settings", nil, nil)
}
