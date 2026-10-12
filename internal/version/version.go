package version

import "runtime/debug"

var current = detect()

func Current() string {
	return current
}

func detect() string {
	info, ok := debug.ReadBuildInfo()
	if ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
