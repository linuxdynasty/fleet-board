//go:build !linux

package config

// maxSocketPath is the size of sun_path in struct sockaddr_un on darwin and
// the BSDs, counting the closing NUL.
const maxSocketPath = 104
