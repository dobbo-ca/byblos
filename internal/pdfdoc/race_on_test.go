//go:build race

package pdfdoc

// raceEnabled is true when the race detector is on. It exists for one test:
// TestRepairDoesNotInflateABombTwice asserts an absolute allocation figure, and
// the race detector roughly doubles allocation, so the figure is meaningless
// there. A relative bound was tried instead and could not work -- the regression
// it guards against inflates the control as well as the subject, so the ratio
// stays put while both double.
const raceEnabled = true
