package hatchery

import (
	"io"
	"log"
)

func SetupAndTeardownTest() func() {
	/*
		Add `defer SetupAndTeardownTest()()` at the beginning of a unit test to add set up and tear down.
		- `SetupAndTeardownTest()` runs the setup code and return a function containing the teardown code.
		- We then `defer` calling the returned function so the teardown code runs after the test returns.
	*/

	/* setup */
	if Config == nil {
		Config = &FullHatcheryConfig{
			// Logger: log.New(os.Stdout, "", log.LstdFlags), // Print all logs (for dev purposes)
			Logger: log.New(io.Discard, "", log.LstdFlags), // Discard all logs
		}
	}

	return func() {
		/* teardown */
	}
}
