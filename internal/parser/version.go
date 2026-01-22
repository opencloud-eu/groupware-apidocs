package parser

import (
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"opencloud.eu/groupware-apidocs/internal/tools"
)

func detectVersion(path string) (string, error) {
	if path == "" {
		path = "."
	}
	r, err := git.PlainOpen(path)
	if err != nil {
		return "", err
	}

	branch := ""
	{
		h, err := r.Head()
		if err != nil {
			return "", err
		}
		branch = h.Name().Short()
	}
	log.Printf("%s:", branch)

	iter, err := r.Tags()
	if err != nil {
		return "", err
	}
	tags := []string{}
	if err := iter.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name().Short()
		if strings.HasPrefix(name, "v") {
			tags = append(tags, name)
		}
		return nil
	}); err != nil {
		return "", err
	}
	slices.SortFunc(tags, compareVersions)
	if strings.HasPrefix(branch, "stable-") {
		parts := strings.Split(branch, "-")
		if len(parts) < 2 {
			return "", fmt.Errorf("branch '%s' does not have 2 parts when split on '-'", branch)
		}
		majorAndMinor := "v" + parts[1]
		prefixed := tools.Filter(tags, func(s string) bool { return strings.HasPrefix(s, majorAndMinor) })
		if len(prefixed) > 0 {
			tags = prefixed
		}
	}

	tag := "v0.0.0"
	if len(tags) > 0 {
		tag = tags[len(tags)-1]
	}

	return tag, nil
}

func parseVersion(version string) (int, int, int, string, error) {
	version = strings.TrimPrefix(version, "v")
	parts := strings.Split(version, "-")
	if len(parts) > 2 {
		return 0, 0, 0, "", fmt.Errorf("version '%s' has more than 2 parts when split on '-'", version)
	}
	main := parts[0]
	pre := ""
	if len(parts) > 1 {
		pre = parts[1]
	}
	parts = strings.Split(main, ".")
	if len(parts) != 3 {
		return 0, 0, 0, "", fmt.Errorf("main version '%s' does not have exactly 3 parts when split on '.'", main)

	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, 0, "", err
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, 0, "", err
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return 0, 0, 0, "", err
	}
	return major, minor, patch, pre, nil
}

func compareVersions(a, b string) int {
	amajor, aminor, apatch, apre, err := parseVersion(a)
	if err != nil {
		return 1
	}
	bmajor, bminor, bpatch, bpre, err := parseVersion(b)
	if err != nil {
		return 1
	}
	d := amajor - bmajor
	if d != 0 {
		return d
	}
	d = aminor - bminor
	if d != 0 {
		return d
	}
	d = apatch - bpatch
	if d != 0 {
		return d
	}
	if apre != "" && bpre == "" {
		return -1
	}
	if apre == "" && bpre != "" {
		return 1
	}
	if apre != "" && bpre != "" {
		return strings.Compare(apre, bpre)
	}
	return 0
}
