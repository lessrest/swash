package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func cmdContext(args []string) {
	if len(args) == 0 {
		cmdContextList()
		return
	}

	subCmd := args[0]
	subArgs := args[1:]

	switch subCmd {
	case "new":
		cmdContextNew()
	case "list":
		cmdContextList()
	case "dir":
		if len(subArgs) == 0 {
			fatal("usage: swash context dir <context_id>")
		}
		cmdContextDir(subArgs[0])
	case "shell":
		if len(subArgs) == 0 {
			fatal("usage: swash context shell <context_id>")
		}
		cmdContextShell(subArgs[0])
	default:
		fatal("unknown context command: %s", subCmd)
	}
}

func cmdContextNew() {
	initBackend()
	defer bk.Close()

	contextID, dir, err := bk.CreateContext(context.Background())
	if err != nil {
		fatal("creating context: %v", err)
	}

	fmt.Printf("%s created\n", contextID)
	fmt.Printf("%s\n", dir)
}

func cmdContextList() {
	initBackend()
	defer bk.Close()

	contexts, err := bk.ListContexts(context.Background())
	if err != nil {
		fatal("listing contexts: %v", err)
	}

	if len(contexts) == 0 {
		fmt.Println("no contexts")
		fmt.Println("swash context new")
		return
	}

	fmt.Printf("%-8s %-20s %s\n", "ID", "CREATED", "DIR")
	for _, c := range contexts {
		created := c.Created.Format("2006-01-02 15:04")
		fmt.Printf("%-8s %-20s %s\n", c.ID, created, c.Dir)
	}
}

func cmdContextDir(contextID string) {
	initBackend()
	defer bk.Close()

	dir, err := bk.GetContextDir(context.Background(), contextID)
	if err != nil {
		fatal("%v", err)
	}

	fmt.Println(dir)
}

func cmdContextShell(contextID string) {
	initBackend()
	defer bk.Close()

	dir, err := bk.GetContextDir(context.Background(), contextID)
	if err != nil {
		fatal("%v", err)
	}

	rcFile, err := createBashRcFile(contextID)
	if err != nil {
		fatal("creating rcfile: %v", err)
	}
	defer os.Remove(rcFile)

	cmd := exec.Command("bash", "--rcfile", rcFile)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "SWASH_CONTEXT="+contextID)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				os.Exit(status.ExitStatus())
			}
		}
		fatal("running shell: %v", err)
	}
}

func cmdPrompt() {
	contextID := os.Getenv("SWASH_CONTEXT")
	if contextID == "" {
		return
	}

	initBackend()
	defer bk.Close()

	runningCount := countRunningInContext(contextID)

	// \001 and \002 are bash's \[ and \] for non-printing chars
	dim := "\001\033[2m\002"
	boldDim := "\001\033[1;2m\002"
	reset := "\001\033[0m\002"

	if runningCount == 0 {
		fmt.Printf("\n%s[swash context %s%s%s]%s", dim, boldDim, contextID, reset+dim, reset)
	} else {
		fmt.Printf("\n%s[swash context %s%s%s; %d running]%s", dim, boldDim, contextID, reset+dim, runningCount, reset)
	}
}

func createBashRcFile(contextID string) (string, error) {
	_ = contextID // context is read from env by swash prompt
	content := `# Swash context shell rcfile
[[ -f ~/.bashrc ]] && source ~/.bashrc

PROMPT_COMMAND=$'PS1="$(swash prompt)\n\\w\\$ "'
`

	f, err := os.CreateTemp("", "swash-rc-*.sh")
	if err != nil {
		return "", err
	}
	defer f.Close()

	if _, err := f.WriteString(content); err != nil {
		os.Remove(f.Name())
		return "", err
	}

	return f.Name(), nil
}
