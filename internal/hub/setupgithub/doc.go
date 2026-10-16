// Package setupgithub is `fleetd setup github` without the terminal.
//
// The cobra wiring in cmd/fleetd owns the one-shot listener and the
// prompts; this package owns everything a test can observe without a
// browser: the App manifest ([Manifest]), the one-time code exchange
// ([Exchange]), the installation list ([ListInstallations]), the 0600
// secret files ([WriteSecrets]) and the fleetd.yaml update
// ([UpdateConfig]). A re-run loads the saved App ([LoadSavedApp]) and
// only lists, selects and saves the installation. A wrong callback
// state is refused by [CheckState]; a used code is a *[CodeError] and
// writes nothing. Errors name the step only, never the secret, the code
// or the path.
package setupgithub
