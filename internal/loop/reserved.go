package loop

import "fmt"

var reservedNames = []struct{ Word, Meaning string }{
	{"podium", "the idle terminal that keeps a run's CAO session alive"},
	{"hall", "something that holds many ensembles"},
	{"conductor", "km itself"},
}

func ReservedNameMessage(name string) (string, bool) {
	for _, reserved := range reservedNames {
		if name == reserved.Word {
			return fmt.Sprintf("%s is km's word for %s; choose another name", name, reserved.Meaning), true
		}
	}
	return "", false
}
