package core

func ValidateEdit(e EditRequest) error {
	return ValidateAdd(AddRequest{Command: e.Command, Directory: e.Directory, Label: e.Label, Priority: e.Priority})
}
