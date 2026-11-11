//go:build race

package service

// raceEnabled lets timing budgets skip under the race detector, which slows
// SQLite-heavy code by an order of magnitude.
const raceEnabled = true
