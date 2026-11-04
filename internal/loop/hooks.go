package loop

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	ConfigFile = ".oko/config.toml"
	VerifyHook = ".oko/verify"
	CheckHook  = ".oko/check"
	ArgsToken  = "{args}"
)

func ResolveHooks(worktree, verifyFlag string) (verify, check string, err error) {
	cfg, _, err := loadConfigFile(filepath.Join(worktree, ConfigFile), ConfigFile)
	if err != nil {
		return "", "", err
	}
	verify, check = cfg.Test.Verify, cfg.Test.Check
	if verify == "" && executable(filepath.Join(worktree, VerifyHook)) {
		verify = "./" + VerifyHook
	}
	if check == "" && executable(filepath.Join(worktree, CheckHook)) {
		check = "./" + CheckHook
	}
	if verifyFlag != "" {
		verify = verifyFlag
	}
	return verify, check, nil
}

func CheckUsage(check string) string {
	const placeholder = "<tests or packages>"
	if strings.Contains(check, ArgsToken) {
		return strings.ReplaceAll(check, ArgsToken, placeholder)
	}
	return check + " " + placeholder
}

func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}
