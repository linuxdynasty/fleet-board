// Command e2eseed writes the shared test fleet (package
// github.com/linuxdynasty/fleet-board/internal/seed) into a fresh state
// folder, for the dashboard's Playwright smoke (`npm run e2e` in web/).
//
// It takes one argument, the state folder, and prints it back. Seeding a
// folder twice fails; the shared fleet is never silently replaced.
package main
