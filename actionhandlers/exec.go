package actionhandlers

import (
	"os/exec"

	streamdeck "github.com/SKAARHOJ/go-streamdeck"
)

type ExecAction struct {
	Command *exec.Cmd
}

func (action *ExecAction) Pressed(btn streamdeck.Button) {
	action.Command.Start()
}

func NewExecAction(command *exec.Cmd) *ExecAction {
	return &ExecAction{Command: command}
}
