package main

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/space-protocol-project/space-server/internal/home"
)

func performPortableInspection(options serverOptions) error {
	if options.portableExport != "" || options.portableImport != "" || options.portableTo != "" || options.backup != "" || options.restore != "" || options.restoreTo != "" || options.homeCheck || options.homeMigrate || options.homeStopped || options.runtimeInfo || options.checkDatabase || options.firstOwner || options.setupCode || options.demo {
		return errors.New("Выберите только проверку архива переноса")
	}
	result, err := home.InspectPortable(options.portableVerify)
	if err != nil {
		return err
	}
	output := options.output
	if output == nil {
		output = os.Stdout
	}
	return json.NewEncoder(output).Encode(result)
}
