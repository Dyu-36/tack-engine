package projects

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRegisterDoesNotLoseConcurrentUpdates(t *testing.T) {
	t.Setenv("CRUSH_GLOBAL_DATA", t.TempDir())
	var wg sync.WaitGroup
	for index := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Register(fmt.Sprintf("project-%02d", index), "data"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	projects, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 32 {
		t.Fatalf("lost registrations: %d", len(projects))
	}
	for index := 1; index < len(projects); index++ {
		if !projects[index-1].LastAccessed.After(projects[index].LastAccessed) {
			t.Fatal("access order is not strict")
		}
	}
}

func TestRegistrationRemainsMostRecentWhenClockMovesBackwards(t *testing.T) {
	t.Setenv("CRUSH_GLOBAL_DATA", t.TempDir())
	future := time.Now().UTC().Add(time.Hour)
	if err := Save(&ProjectList{Projects: []Project{{Path: "old", LastAccessed: future}}}); err != nil {
		t.Fatal(err)
	}
	if err := Register("new", "data"); err != nil {
		t.Fatal(err)
	}
	projects, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 || projects[0].Path != "new" || !projects[0].LastAccessed.After(future) {
		t.Fatalf("incorrect access ordering: %+v", projects)
	}
}
