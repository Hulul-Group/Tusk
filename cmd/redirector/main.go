// Command redirector: Hot-path redirector only (role redirector); listens on REDIRECTOR_PORT.
package main

import "github.com/Hulul-Group/Tusk/cmd/internal/app"

func main() { app.Main("redirector", []string{"redirector"}) }
