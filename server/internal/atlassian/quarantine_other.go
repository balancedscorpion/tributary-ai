//go:build !darwin

package atlassian

func removeQuarantineAttributes(string) error {
	return nil
}
