//go:build !darwin

package sharepoint

func removeQuarantineAttributes(string) error {
	return nil
}
