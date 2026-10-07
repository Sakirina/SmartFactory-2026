// Package buildinfo exposes executable identity without opening a database.
package buildinfo

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"runtime"
	"strconv"

	"competition2026/product/platform/pkg/model"
)

// Release builds set these variables with -ldflags -X.
var Version = "1.1.0-dev"
var SourceSHA256 = ""
var MigrationMinimum = "11"
var MigrationMaximum = "11"

func Current(program string) model.ProgramBuild {
	minimum, _ := strconv.ParseInt(MigrationMinimum, 10, 64)
	maximum, _ := strconv.ParseInt(MigrationMaximum, 10, 64)
	return model.ProgramBuild{Program: program, Version: Version, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, MigrationMinimum: minimum, MigrationMaximum: maximum, RuleFormats: []string{model.ContractVersion}, ConfigurationFormats: []string{"smartfactory-configuration-v1"}, SourceSHA256: SourceSHA256}
}
func ExecutableSHA256() (string, error) {
	path, e := os.Executable()
	if e != nil {
		return "", e
	}
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
