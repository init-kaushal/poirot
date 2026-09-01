package main

import "fmt"

// ExitError carries a desired process exit code up to main().
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("exit code %d", e.Code) }
