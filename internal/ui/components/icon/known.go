package icon

// Known reports whether name is one of the vendored lucide icons, so a page
// can refuse an unknown name when it is configured instead of when it renders.
func Known(name string) bool {
	_, ok := internalSvgData[name]
	return ok
}
