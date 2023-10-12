/*

/!\ DO NOT CHANGE THIS FILE'S NAME!

This is ridiculous, but we need this file to be _first_ (alphabetically) when running the tests so that `TestMain`
runs before the unit tests.
`TestMain` is the official way to implement global test setup and teardown, but there can only be one `TestMain`,
so we can't copy/paste it in each test file either.
*/

package hatchery

import (
	"fmt"
	"log"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	fmt.Println("Setting up tests...")
	Config = &FullHatcheryConfig{
		Logger: log.New(os.Stdout, "", log.LstdFlags), // Print all logs (for dev purposes)
		// Logger: log.New(io.Discard, "", log.LstdFlags), // Discard all logs TODO uncomment
	}
	code := m.Run()
	os.Exit(code)
}
