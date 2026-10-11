package daemon

// SetAfterBind makes Run with o call f for each TCP listener bind opens (not
// the admin socket), with the listener's name and the server that serves it,
// before any of them serves.
func SetAfterBind(o *Options, f func(name string, srv any)) { o.afterBind = f }
