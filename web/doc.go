// Package web is the fleet-board dashboard: the React app under web/src,
// built with Vite to web/dist and embedded in fleetd (04-technology-review,
// "UI build and serve").
//
// web/dist holds build output and is not committed, except the placeholder
// web/dist/index.html, which keeps `go build` working on a checkout without
// Node. `npm run build` in web/ replaces it with the real page; the
// end-to-end smoke (`npm run e2e`) restores the placeholder afterwards, so a
// built page never lands in git.
package web
