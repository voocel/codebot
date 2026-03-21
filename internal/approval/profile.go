package approval

import (
	"fmt"
	"strings"
)

type Profile string

const (
	ProfileOff      Profile = "off"
	ProfileBalanced Profile = "balanced"
	ProfileStrict   Profile = "strict"
)

func ParseProfile(raw string) (Profile, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return ProfileBalanced, nil
	case string(ProfileOff):
		return ProfileOff, nil
	case string(ProfileStrict):
		return ProfileStrict, nil
	case string(ProfileBalanced):
		return ProfileBalanced, nil
	default:
		return "", fmt.Errorf("invalid approval profile %q (allowed: off, balanced, strict)", raw)
	}
}
