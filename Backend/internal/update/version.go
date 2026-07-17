package update

import (
	"fmt"
	"strconv"
	"strings"
)

type version struct {
	major, minor, patch int
	prerelease          []string
}

func parseVersion(raw string) (version, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "v"))
	coreAndPre := strings.SplitN(strings.SplitN(raw, "+", 2)[0], "-", 2)
	parts := strings.Split(coreAndPre[0], ".")
	if len(parts) != 3 {
		return version{}, fmt.Errorf("version must use major.minor.patch")
	}
	numbers := make([]int, 3)
	for index, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return version{}, fmt.Errorf("invalid version component")
		}
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return version{}, fmt.Errorf("invalid version component")
		}
		numbers[index] = value
	}
	parsed := version{major: numbers[0], minor: numbers[1], patch: numbers[2]}
	if len(coreAndPre) == 2 {
		if coreAndPre[1] == "" {
			return version{}, fmt.Errorf("empty prerelease")
		}
		parsed.prerelease = strings.Split(coreAndPre[1], ".")
		for _, part := range parsed.prerelease {
			if part == "" {
				return version{}, fmt.Errorf("invalid prerelease")
			}
		}
	}
	return parsed, nil
}

func compareVersions(left, right string) (int, error) {
	a, err := parseVersion(left)
	if err != nil {
		return 0, err
	}
	b, err := parseVersion(right)
	if err != nil {
		return 0, err
	}
	for _, pair := range [][2]int{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if pair[0] < pair[1] {
			return -1, nil
		}
		if pair[0] > pair[1] {
			return 1, nil
		}
	}
	if len(a.prerelease) == 0 && len(b.prerelease) == 0 {
		return 0, nil
	}
	if len(a.prerelease) == 0 {
		return 1, nil
	}
	if len(b.prerelease) == 0 {
		return -1, nil
	}
	for index := 0; index < len(a.prerelease) && index < len(b.prerelease); index++ {
		if result := comparePrerelease(a.prerelease[index], b.prerelease[index]); result != 0 {
			return result, nil
		}
	}
	if len(a.prerelease) < len(b.prerelease) {
		return -1, nil
	}
	if len(a.prerelease) > len(b.prerelease) {
		return 1, nil
	}
	return 0, nil
}

func comparePrerelease(left, right string) int {
	leftNumber, leftErr := strconv.Atoi(left)
	rightNumber, rightErr := strconv.Atoi(right)
	if leftErr == nil && rightErr == nil {
		if leftNumber < rightNumber {
			return -1
		}
		if leftNumber > rightNumber {
			return 1
		}
		return 0
	}
	if leftErr == nil {
		return -1
	}
	if rightErr == nil {
		return 1
	}
	return strings.Compare(left, right)
}
