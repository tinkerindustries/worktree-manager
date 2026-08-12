package main

import (
	"fmt"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
)

func main() {
	for _, p := range []int{8201, 8202, 8203, 8204, 8205, 8206, 8207} {
		err := platform.ProbeBind(p)
		fmt.Printf("%d: %v\n", p, err)
	}
	time.Sleep(100 * time.Millisecond)
	for _, p := range []int{8201, 8207} {
		err := platform.ProbeBind(p)
		fmt.Printf("again %d: %v\n", p, err)
	}
}
