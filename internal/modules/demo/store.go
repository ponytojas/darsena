// Package demo implements bounded, volatile simulations. It never accesses infrastructure.
package demo

import (
	"context"
	"crypto/rand"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Application struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Repository   string    `json:"repository"`
	Branch       string    `json:"branch"`
	Context      string    `json:"context"`
	Port         int       `json:"port"`
	URL          string    `json:"url"`
	Status       string    `json:"status"`
	Version      int       `json:"version"`
	CreatedAt    time.Time `json:"created_at"`
	OperationIDs []string  `json:"operation_ids"`
}
type Step struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}
type Log struct {
	Sequence int       `json:"sequence"`
	Time     time.Time `json:"time"`
	Message  string    `json:"message"`
}
type Operation struct {
	ID            string    `json:"id"`
	ApplicationID string    `json:"application_id"`
	Status        string    `json:"status"`
	Scenario      string    `json:"scenario"`
	Revision      int       `json:"revision"`
	CreatedAt     time.Time `json:"created_at"`
	Steps         []Step    `json:"steps"`
	Logs          []Log     `json:"logs"`
	Error         string    `json:"error"`
}
type CreateInput struct {
	Name       string `json:"name"`
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	Context    string `json:"context"`
	Port       int    `json:"port"`
	Scenario   string `json:"scenario"`
}
type Store struct {
	mu         sync.RWMutex
	apps       []Application
	operations []Operation
	cancel     context.CancelFunc
	done       chan struct{}
	interval   time.Duration
}

var slug = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)
var repo = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func ValidScenario(s string) bool {
	return s == "success" || s == "build_failure" || s == "health_failure"
}
func Validate(in CreateInput) error {
	if !slug.MatchString(in.Name) {
		return fmt.Errorf("El nombre debe tener entre 2 y 40 caracteres: letras minúsculas, números y guiones; empieza por una letra.")
	}
	if !repo.MatchString(in.Repository) || len(in.Repository) > 160 {
		return fmt.Errorf("Indica el repositorio como propietario/nombre.")
	}
	if strings.TrimSpace(in.Branch) == "" || len(in.Branch) > 100 {
		return fmt.Errorf("Indica una rama de hasta 100 caracteres.")
	}
	if in.Port < 1 || in.Port > 65535 {
		return fmt.Errorf("El puerto debe estar entre 1 y 65535.")
	}
	if in.Context == "" || len(in.Context) > 200 || strings.HasPrefix(in.Context, "/") || strings.Contains(in.Context, "..") {
		return fmt.Errorf("El contexto debe ser una ruta relativa dentro del repositorio.")
	}
	if !ValidScenario(in.Scenario) {
		return fmt.Errorf("Escenario de demostración desconocido.")
	}
	return nil
}
func New(interval time.Duration) *Store {
	s := &Store{interval: interval, apps: []Application{}, operations: []Operation{}}
	for i, name := range []string{"bitacora", "atlas-api", "archivo"} {
		a := Application{ID: name, Name: name, Repository: "demo/" + name, Branch: "main", Context: ".", Port: 3000, URL: "https://" + name + ".example.invalid", Status: "healthy", Version: 1, CreatedAt: time.Now().UTC().Add(-time.Duration(i+1) * time.Hour), OperationIDs: []string{}}
		s.apps = append(s.apps, a)
	}
	return s
}
func (s *Store) ID() string             { return "demo" }
func (s *Store) Dependencies() []string { return nil }
func (s *Store) Start(ctx context.Context) error {
	ctx, s.cancel = context.WithCancel(ctx)
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				s.interrupt()
				return
			case <-ticker.C:
				s.advance()
			}
		}
	}()
	return nil
}
func (s *Store) Stop(ctx context.Context) error {
	if s.cancel == nil {
		return nil
	}
	s.cancel()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func id() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", b)
}
func (s *Store) Apps() []Application {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Application, len(s.apps))
	for i, a := range s.apps {
		a.OperationIDs = append([]string{}, a.OperationIDs...)
		out[i] = a
	}
	return out
}
func (s *Store) App(id string) (Application, bool) {
	for _, a := range s.Apps() {
		if a.ID == id {
			return a, true
		}
	}
	return Application{}, false
}
func clone(o Operation) Operation {
	o.Steps = append([]Step{}, o.Steps...)
	o.Logs = append([]Log{}, o.Logs...)
	return o
}
func (s *Store) Operations() []Operation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Operation, len(s.operations))
	for i, o := range s.operations {
		out[i] = clone(o)
	}
	return out
}
func (s *Store) Operation(id string) (Operation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, o := range s.operations {
		if o.ID == id {
			return clone(o), true
		}
	}
	return Operation{}, false
}
func (s *Store) Create(in CreateInput) (Application, Operation, error) {
	if err := Validate(in); err != nil {
		return Application{}, Operation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.apps) >= 100 || len(s.operations) >= 300 {
		return Application{}, Operation{}, fmt.Errorf("Límite de la demostración alcanzado. Reinicia el servidor para restablecer los datos.")
	}
	for _, a := range s.apps {
		if a.Name == in.Name {
			return Application{}, Operation{}, fmt.Errorf("Ya existe una aplicación con ese nombre.")
		}
	}
	a := Application{ID: id(), Name: in.Name, Repository: in.Repository, Branch: in.Branch, Context: in.Context, Port: in.Port, URL: "https://" + in.Name + ".example.invalid", Status: "pending", CreatedAt: time.Now().UTC(), OperationIDs: []string{}}
	s.apps = append(s.apps, a)
	o := s.deployLocked(len(s.apps)-1, in.Scenario)
	a = s.apps[len(s.apps)-1]
	a.OperationIDs = append([]string{}, a.OperationIDs...)
	return a, clone(o), nil
}
func (s *Store) Deploy(appID, scenario string) (Operation, error) {
	if !ValidScenario(scenario) {
		return Operation{}, fmt.Errorf("Escenario de demostración desconocido.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.operations) >= 300 {
		return Operation{}, fmt.Errorf("Límite de demostración alcanzado; reinicia el servidor.")
	}
	for i, a := range s.apps {
		if a.ID == appID {
			for _, o := range s.operations {
				if o.ApplicationID == appID && (o.Status == "queued" || o.Status == "running") {
					return Operation{}, fmt.Errorf("Ya hay un despliegue en curso para esta aplicación.")
				}
			}
			return clone(s.deployLocked(i, scenario)), nil
		}
	}
	return Operation{}, fmt.Errorf("Aplicación no encontrada.")
}
func (s *Store) deployLocked(i int, scenario string) Operation {
	o := Operation{ID: id(), ApplicationID: s.apps[i].ID, Scenario: scenario, Status: "queued", Revision: 1, CreatedAt: time.Now().UTC(), Steps: []Step{}, Logs: []Log{}}
	for _, name := range []string{"Validar configuración", "Construir imagen", "Publicar artefacto", "Preparar servicio", "Comprobar salud", "Verificar ruta"} {
		o.Steps = append(o.Steps, Step{Name: name, Status: "pending", Message: ""})
	}
	logLine(&o, "Solicitud aceptada · simulación local; no se ejecutan comandos externos.")
	s.apps[i].OperationIDs = append([]string{o.ID}, s.apps[i].OperationIDs...)
	s.operations = append([]Operation{o}, s.operations...)
	return o
}
func logLine(o *Operation, message string) {
	o.Logs = append(o.Logs, Log{Sequence: len(o.Logs) + 1, Time: time.Now().UTC(), Message: message})
	if len(o.Logs) > 200 {
		o.Logs = o.Logs[len(o.Logs)-200:]
	}
}
func (s *Store) advance() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for oi := range s.operations {
		o := &s.operations[oi]
		if o.Status != "queued" && o.Status != "running" {
			continue
		}
		o.Status = "running"
		o.Revision++
		for i := range o.Steps {
			step := &o.Steps[i]
			if step.Status == "succeeded" {
				continue
			}
			if step.Status == "pending" {
				step.Status = "running"
				logLine(o, step.Name+" · etapa simulada iniciada.")
				break
			}
			if (i == 1 && o.Scenario == "build_failure") || (i == 4 && o.Scenario == "health_failure") {
				step.Status = "failed"
				o.Status = "failed"
				if i == 1 {
					o.Error = "La construcción simulada ha fallado. Revisa el Dockerfile y vuelve a intentarlo."
				} else {
					o.Error = "El servicio simulado no responde al healthcheck. Revisa el puerto y vuelve a intentarlo."
				}
				step.Message = o.Error
				logLine(o, o.Error)
				s.finish(o, false)
				break
			}
			step.Status = "succeeded"
			step.Message = "Comprobación simulada completada."
			logLine(o, step.Name+" · completado.")
			if i == len(o.Steps)-1 {
				o.Status = "succeeded"
				logLine(o, "Despliegue simulado completado. La URL de ejemplo no es navegable.")
				s.finish(o, true)
			}
			break
		}
	}
}
func (s *Store) finish(o *Operation, success bool) {
	for i := range s.apps {
		a := &s.apps[i]
		if a.ID == o.ApplicationID {
			if success {
				a.Status = "healthy"
				a.Version++
			} else if a.Version == 0 {
				a.Status = "failed"
			}
			return
		}
	}
}
func (s *Store) interrupt() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.operations {
		o := &s.operations[i]
		if o.Status == "queued" || o.Status == "running" {
			o.Status = "interrupted"
			o.Revision++
			o.Error = "El servidor de demostración se ha detenido."
			logLine(o, o.Error)
		}
	}
}
