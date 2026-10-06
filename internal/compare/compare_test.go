package compare

import "testing"

func TestFlattenKeysByObject(t *testing.T) {
	a, err := Flatten(map[string]string{
		"t/a.yaml": "apiVersion: v1\nkind: Service\nmetadata:\n  name: web\n  namespace: apps\nspec:\n  ports:\n    - port: 80\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Flatten(map[string]string{
		"t/b.yaml": "  \n---\napiVersion: v1\nkind: Service\nmetadata:\n  name: web\n  namespace: apps\nspec:\n  ports:\n    - port: 80\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !Equal(a, b, nil) {
		t.Errorf("same object in another template should compare equal:\n%v\n%v", a, b)
	}
	if a["Service/apps/web:.spec.ports[0].port"] != "80" {
		t.Errorf("snapshot = %v", a)
	}
}

func TestNoiseMasksRandomFields(t *testing.T) {
	a := Snapshot{"Secret/s:.data.p": `"x1"`, "Deployment/d:.spec.replicas": "1"}
	b := Snapshot{"Secret/s:.data.p": `"y2"`, "Deployment/d:.spec.replicas": "1"}
	noise := Noise(a, b)
	if !noise["Secret/s:.data.p"] || len(noise) != 1 {
		t.Errorf("noise = %v", noise)
	}
	c := Snapshot{"Secret/s:.data.p": `"z3"`, "Deployment/d:.spec.replicas": "2"}
	if Equal(a, c, noise) {
		t.Error("a real change was masked")
	}
	c["Deployment/d:.spec.replicas"] = "1"
	if !Equal(a, c, noise) {
		t.Error("noise was not masked")
	}
}
