package util

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var rxVersion = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(.*)`)

type Version struct {
	V      [3]uint32
	Suffix string
}

func (v *Version) String() string {
	return fmt.Sprintf("%d.%d.%d%s", v.V[0], v.V[1], v.V[2], v.Suffix)
}

func (v *Version) Less(r *Version) bool {
	return v.V[0] < r.V[0] ||
		(v.V[0] == r.V[0] && v.V[1] < r.V[1]) ||
		(v.V[0] == r.V[0] && v.V[1] == r.V[1] && v.V[2] < r.V[2])
}

func ParseVersion(vsn string) (*Version, error) {
	m := rxVersion.FindStringSubmatch(strings.TrimSpace(vsn))
	if m == nil {
		return nil, fmt.Errorf("could not parse version %q", vsn)
	}

	v := &Version{
		Suffix: m[4],
	}

	for i := 0; i < 3; i++ {
		d, err := strconv.ParseUint(m[i+1], 10, 32)
		if err != nil {
			return nil, err
		}

		v.V[i] = uint32(d)
	}

	return v, nil
}
