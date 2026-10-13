package normalize

import "testing"

func TestSpanish(t *testing.T) {
	nums := map[uint64]string{
		0: "cero", 1: "uno", 15: "quince", 16: "dieciséis", 21: "veintiuno", 22: "veintidós", 31: "treinta y uno",
		100: "cien", 101: "ciento uno", 115: "ciento quince", 200: "doscientos", 500: "quinientos",
		1000: "mil", 1001: "mil uno", 2000: "dos mil", 21000: "veintiún mil", 101000: "ciento un mil",
		1000000: "un millón", 2000000: "dos millones", 1500000: "un millón quinientos mil",
		1000000000: "mil millones", 2024: "dos mil veinticuatro",
	}
	for n, want := range nums {
		if got := esCardinal(n); got != want {
			t.Errorf("esCardinal(%d) = %s, want %s", n, got, want)
		}
	}
	run(t, "es-ES", []tc{
		{"Tengo 3 manzanas.", "Tengo tres manzanas."},
		{"Son 10.000 personas", "Son diez mil personas"},
		{"Vale 3,5 y 3,14 y 2,05", "Vale tres coma cinco y tres coma catorce y dos coma cero cinco"},
		{"Un 25 % más", "Un veinticinco por ciento más"},
		{"El 1º y la 2ª", "El primero y la segunda"},
		{"A las 3:30 o a la 1:00", "A las tres y treinta o a la una en punto"},
		{"El 05/03/2024", "El cinco de marzo de dos mil veinticuatro"},
		{"Cuesta 5 €", "Cuesta cinco euros"},
		{"Cuesta 1 € o 21 €", "Cuesta un euro o veintiún euros"},
		{"Pagó 3,50 € y $2", "Pagó tres euros con cincuenta céntimos y dos dólares"},
		{"Ganó 1.000.000 €", "Ganó un millón de euros"},
		{"De 3–5 días", "De tres a cinco días"},
		{"El Sr. García y la Dra. López, etc.", "El señor García y la doctora López, etcétera."},
		{"Hace -3 °C", "Hace menos tres grados Celsius"},
	})
}

func TestFrench(t *testing.T) {
	nums := map[uint64]string{
		0: "zéro", 1: "un", 17: "dix-sept", 21: "vingt et un", 22: "vingt-deux", 70: "soixante-dix",
		71: "soixante et onze", 72: "soixante-douze", 80: "quatre-vingts", 81: "quatre-vingt-un",
		90: "quatre-vingt-dix", 99: "quatre-vingt-dix-neuf", 100: "cent", 101: "cent un", 200: "deux cents",
		201: "deux cent un", 1000: "mille", 2000: "deux mille", 80000: "quatre-vingt mille", 200000: "deux cent mille",
		1000000: "un million", 2000000: "deux millions", 2024: "deux mille vingt-quatre", 1000000000: "un milliard",
	}
	for n, want := range nums {
		if got := frCardinal(n); got != want {
			t.Errorf("frCardinal(%d) = %s, want %s", n, got, want)
		}
	}
	run(t, "fr-FR", []tc{
		{"J'ai 3 pommes.", "J'ai trois pommes."},
		{"Il y a 10 000 habitants et 2 500 voitures", "Il y a dix mille habitants et deux mille cinq cents voitures"},
		{"Pi vaut 3,14", "Pi vaut trois virgule quatorze"},
		{"Une hausse de 12 %", "Une hausse de douze pour cent"},
		{"Le 1er et la 1re et le 2e et le 21e", "Le premier et la première et le deuxième et le vingt et unième"},
		{"Rendez-vous à 15h30 ou 9 h ou 21:00", "Rendez-vous à quinze heures trente ou neuf heures ou vingt et une heures"},
		{"Le 01/03/2024", "Le premier mars deux mille vingt-quatre"},
		{"Ça coûte 5 € ou 5,50 € ou 1 €", "Ça coûte cinq euros ou cinq euros cinquante ou un euro"},
		{"Prix : 2 000 000 €", "Prix : deux millions d'euros"},
		{"Prix : $3", "Prix : trois dollars"},
		{"De 3–5 jours", "De trois à cinq jours"},
		{"M. Dupont et Mme Martin, n° 5, etc.", "Monsieur Dupont et Madame Martin, numéro cinq, et cetera."},
		{"Il fait 20 °C", "Il fait vingt degrés Celsius"},
	})
}

func TestGerman(t *testing.T) {
	nums := map[uint64]string{
		0: "null", 1: "eins", 11: "elf", 16: "sechzehn", 21: "einundzwanzig", 30: "dreißig", 99: "neunundneunzig",
		100: "einhundert", 101: "einhunderteins", 111: "einhundertelf", 1000: "eintausend", 1001: "eintausendeins",
		2024: "zweitausendvierundzwanzig", 21000: "einundzwanzigtausend", 1000000: "eine Million",
		2300000: "zwei Millionen dreihunderttausend", 1000000000: "eine Milliarde",
	}
	for n, want := range nums {
		if got := deCardinal(n); got != want {
			t.Errorf("deCardinal(%d) = %s, want %s", n, got, want)
		}
	}
	run(t, "de-DE", []tc{
		{"Ich habe 3 Äpfel.", "Ich habe drei Äpfel."},
		{"Es sind 10.000 Leute", "Es sind zehntausend Leute"},
		{"Pi ist 3,14", "Pi ist drei Komma eins vier"},
		{"Ein Plus von 12 %", "Ein Plus von zwölf Prozent"},
		{"Um 15:30 Uhr oder 9:00 oder 1:00 Uhr", "Um fünfzehn Uhr dreißig oder neun Uhr oder ein Uhr"},
		{"Um 15.30 Uhr", "Um fünfzehn Uhr dreißig"},
		{"Am 5.3.2024 und am 1. Mai", "Am fünften März zweitausendvierundzwanzig und am ersten Mai"},
		{"Datum: 03.10.1990", "Datum: dritter Oktober neunzehnhundertneunzig"},
		{"Kapitel 3.2. beginnt", "Kapitel 3.2. beginnt"},
		{"Kostet 5 € oder 5,50 € oder 1 €", "Kostet fünf Euro oder fünf Euro fünfzig oder ein Euro"},
		{"Kostet $20", "Kostet zwanzig Dollar"},
		{"Von 3–5 Tagen", "Von drei bis fünf Tagen"},
		{"z. B. Dr. Müller, Nr. 5 usw.", "zum Beispiel Doktor Müller, Nummer fünf und so weiter."},
		{"Es sind -3 °C", "Es sind minus drei Grad Celsius"},
	})
}
