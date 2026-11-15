package common

// NormalizeSystemName upgrades only former product defaults. Custom site names
// remain unchanged, and loading an old name does not rewrite the database.
func NormalizeSystemName(name string) string {
	switch name {
	case "", "New API", "NewAPI", "RenewAPI":
		return DefaultSystemName
	default:
		return name
	}
}
