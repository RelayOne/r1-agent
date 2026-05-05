package lanes

// decideMode is the adaptive layout decision algorithm. It returns the
// layout mode plus the column count for grid render.
//
// Per specs/tui-lanes.md §"Layout algorithm":
//
//	cols_can_fit = floor(width / LANE_MIN_WIDTH)
//	cols_can_fit = clamp(cols_can_fit, 1, min(COLS_MAX, n))
//	if cols_can_fit < 2: stack
//	else:                columns
//
// Special cases:
//   - n == 0      → modeEmpty, cols=1
//   - currentMode == modeFocus AND n > 0 → preserve focus mode (the
//     'esc' key is the only way to leave focus; resize must not knock
//     the user out of it). cols is irrelevant in focus mode.
//
// Per spec checklist items 13 + 22.
func decideMode(width, height, n int, currentMode layoutMode) (cols int, mode layoutMode) {
	_ = height // height is reserved for future row-cap logic.

	if n == 0 {
		return 1, modeEmpty
	}

	// Focus mode is sticky across resize. The renderer does its own
	// 65/35 split so the cols return value is unused — we still report
	// 1 to keep callers' grid logic well-defined.
	if currentMode == modeFocus {
		return 1, modeFocus
	}

	colsCanFit := width / LANE_MIN_WIDTH
	if colsCanFit < 1 {
		colsCanFit = 1
	}
	cap1 := COLS_MAX
	if n < cap1 {
		cap1 = n
	}
	if colsCanFit > cap1 {
		colsCanFit = cap1
	}

	if colsCanFit < 2 {
		return 1, modeStack
	}
	return colsCanFit, modeColumns
}
