//go:build !darwin

package browsercontrol

func removeQuarantineAttributes(string) error {
	return nil
}
