// Command worker: Visit-event worker + expired-link sweeper (role worker).
package main

import "github.com/Hulul-Group/Tusk/cmd/internal/app"

func main() { app.Main("worker", []string{"worker"}) }
