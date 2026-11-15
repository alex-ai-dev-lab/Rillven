package common

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// envValue accepts the previous project prefix for existing installations.
// A nonempty RILLVEN value takes precedence; unrelated variables are unchanged.
// State encryption keys use a paired lookup instead (see RelayStateCipher).
func envValue(name string) string {
	value := os.Getenv(name)
	if value == "" && strings.HasPrefix(name, "RILLVEN_") {
		value = os.Getenv("RENEWAPI_" + strings.TrimPrefix(name, "RILLVEN_"))
	}
	return value
}

func GetEnvOrDefault(env string, defaultValue int) int {
	value := envValue(env)
	if value == "" {
		return defaultValue
	}
	num, err := strconv.Atoi(value)
	if err != nil {
		SysError(fmt.Sprintf("failed to parse %s: %s, using default value: %d", env, err.Error(), defaultValue))
		return defaultValue
	}
	return num
}

func GetEnvOrDefaultString(env string, defaultValue string) string {
	value := envValue(env)
	if value == "" {
		return defaultValue
	}
	return value
}

func GetEnvOrDefaultBool(env string, defaultValue bool) bool {
	value := envValue(env)
	if value == "" {
		return defaultValue
	}
	b, err := strconv.ParseBool(value)
	if err != nil {
		SysError(fmt.Sprintf("failed to parse %s: %s, using default value: %t", env, err.Error(), defaultValue))
		return defaultValue
	}
	return b
}
