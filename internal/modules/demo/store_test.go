package demo

import (
	"context"
	"testing"
	"time"
)

func input(scenario string) CreateInput {
	return CreateInput{Name: "test-app", Repository: "demo/test-app", Branch: "main", Context: ".", Port: 3000, Scenario: scenario}
}
func complete(s *Store, id string) Operation {
	for range 15 {
		s.advance()
	}
	o, _ := s.Operation(id)
	return o
}
func TestScenarios(t *testing.T) {
	for _, scenario := range []string{"success", "build_failure", "health_failure"} {
		t.Run(scenario, func(t *testing.T) {
			s := New(time.Second)
			a, o, err := s.Create(input(scenario))
			if err != nil {
				t.Fatal(err)
			}
			if o.Status != "queued" {
				t.Fatal(o.Status)
			}
			done := complete(s, o.ID)
			got, _ := s.App(a.ID)
			if scenario == "success" {
				if done.Status != "succeeded" || got.Version != 1 {
					t.Fatalf("%+v %+v", done, got)
				}
			} else if done.Status != "failed" || got.Version != 0 || done.Error == "" {
				t.Fatalf("%+v %+v", done, got)
			}
		})
	}
}
func TestFailedBuildPreservesAppliedVersion(t *testing.T) {
	s := New(time.Second)
	a, o, _ := s.Create(input("success"))
	complete(s, o.ID)
	o, err := s.Deploy(a.ID, "build_failure")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Deploy(a.ID, "success"); err == nil {
		t.Fatal("concurrent deployment accepted")
	}
	complete(s, o.ID)
	got, _ := s.App(a.ID)
	if got.Version != 1 || got.Status != "healthy" {
		t.Fatalf("lost applied state: %+v", got)
	}
}
func TestValidationAndCopies(t *testing.T) {
	s := New(time.Second)
	in := input("unknown")
	if _, _, err := s.Create(in); err == nil {
		t.Fatal("invalid scenario accepted")
	}
	in = input("success")
	in.Context = "../host"
	if _, _, err := s.Create(in); err == nil {
		t.Fatal("invalid path accepted")
	}
	a, o, _ := s.Create(input("success"))
	o.Steps[0].Name = "tampered"
	a.OperationIDs[0] = "tampered"
	original, _ := s.Operation(o.ID)
	app, _ := s.App(a.ID)
	if original.Steps[0].Name == "tampered" || app.OperationIDs[0] == "tampered" {
		t.Fatal("mutable snapshot")
	}
	if _, _, err := s.Create(input("success")); err == nil {
		t.Fatal("duplicate name accepted")
	}
}
func TestShutdownInterrupts(t *testing.T) {
	s := New(time.Hour)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, o, _ := s.Create(input("success"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Operation(o.ID)
	if got.Status != "interrupted" {
		t.Fatal(got.Status)
	}
}
