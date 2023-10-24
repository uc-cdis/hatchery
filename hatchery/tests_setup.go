package hatchery

import (
	"io"
	"log"
)

func SetupTest() func() {
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
