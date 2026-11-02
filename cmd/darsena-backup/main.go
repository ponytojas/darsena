// darsena-backup operates only on offline data. It never reads age identities.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ponytojas/darsena/internal/platform/backup"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("uso: darsena-backup create --state RUTA [--vault RUTA] [--intent RUTA] --destination DIRECTORIO | restore --source DIRECTORIO --destination DIRECTORIO")
	}
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var state, vault, intent, source, destination string
	switch args[0] {
	case "create":
		flags.StringVar(&state, "state", "", "")
		flags.StringVar(&vault, "vault", "", "")
		flags.StringVar(&intent, "intent", "", "")
		flags.StringVar(&destination, "destination", "", "")
	case "restore":
		flags.StringVar(&source, "source", "", "")
		flags.StringVar(&destination, "destination", "", "")
	default:
		return errors.New("comando desconocido; usa create o restore")
	}
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || destination == "" {
		return errors.New("opciones inválidas; indica las rutas necesarias")
	}
	var err error
	if args[0] == "create" {
		err = backup.Create(ctx, backup.Sources{State: state, Vault: vault, Intent: intent}, destination)
	} else {
		err = backup.Restore(ctx, source, destination)
	}
	switch {
	case err == nil:
		fmt.Fprintln(os.Stdout, "Operación completada. La identidad age se conserva por separado.")
		return nil
	case errors.Is(err, backup.ErrBusy):
		return errors.New("estado ocupado: detén el worker y todos los demás usuarios de los almacenes")
	case errors.Is(err, backup.ErrInvalid):
		return errors.New("rutas inválidas, inseguras o destino ya existente; usa directorios privados y rutas sin enlaces")
	default:
		return errors.New("copia incompleta o inválida; conserva los originales y revisa la documentación")
	}
}
