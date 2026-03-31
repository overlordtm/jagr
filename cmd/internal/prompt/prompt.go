package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/overlordtm/jagr/internal/agent"
)

func main() {
	if len(os.Args) < 2 {
		roles := availableRoles()
		fmt.Fprintf(os.Stderr, "Usage: %s <role>\n\nAvailable roles:\n  %s\n", os.Args[0], strings.Join(roles, "\n  "))
		os.Exit(1)
	}

	role := os.Args[1]
	prompt, err := agent.GetPrompt(role, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(prompt)
}

func availableRoles() []string {
	roles := []string{
		"investigator",
		"reporter",
		"phase_UserAccess",
		"phase_Persistence",
		"phase_Network",
		"phase_Filesystem",
		"phase_LogAnalysis",
	}
	sort.Strings(roles)
	return roles
}
