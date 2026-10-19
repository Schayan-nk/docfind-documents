package format

// Jalali (Solar Hijri) calendar conversion.
//
// Iranian business documents are dated in the Jalali calendar, so a service
// that generates Persian paperwork from a Gregorian ISO date is only half
// useful. The algorithm below is the standard astronomical implementation
// (Borkowski's leap-year cycle analysis, the same one behind jalaali-js and
// PHP's jdate); it is exact for the years 1178–1633 Jalali, which comfortably
// covers any date a purchase order will ever carry.

var breaks = [...]int{
	-61, 9, 38, 199, 426, 686, 756, 818, 1111, 1181, 1210,
	1635, 2060, 2097, 2192, 2262, 2324, 2394, 2456, 3178,
}

type jalCalResult struct {
	leap  int // years since the last leap year
	gy    int // Gregorian year of Farvardin 1 of this Jalali year
	march int // Gregorian day-of-March of Farvardin 1
}

func jalCal(jy int) jalCalResult {
	gy := jy + 621
	leapJ := -14
	jp := breaks[0]

	jump := 0
	for i := 1; i < len(breaks); i++ {
		jm := breaks[i]
		jump = jm - jp
		if jy < jm {
			break
		}
		leapJ += (jump/33)*8 + (jump%33)/4
		jp = jm
	}
	n := jy - jp

	leapJ += (n/33)*8 + ((n%33)+3)/4
	if jump%33 == 4 && jump-n == 4 {
		leapJ++
	}

	leapG := gy/4 - ((gy/100+1)*3)/4 - 150
	march := 20 + leapJ - leapG

	if jump-n < 6 {
		n = n - jump + ((jump+4)/33)*33
	}
	leap := ((n+1)%33 - 1) % 4
	if leap == -1 {
		leap = 4
	}
	return jalCalResult{leap: leap, gy: gy, march: march}
}

func div(a, b int) int { return a / b }

// gregorianToJDN converts a Gregorian date to a Julian Day Number.
func gregorianToJDN(gy, gm, gd int) int {
	d := div((gy+div(gm-8, 6)+100100)*1461, 4) +
		div(153*((gm+9)%12)+2, 5) + gd - 34840408
	d = d - div(div(gy+100100+div(gm-8, 6), 100)*3, 4) + 752
	return d
}

// jdnToGregorian converts a Julian Day Number back to a Gregorian date.
func jdnToGregorian(jdn int) (gy, gm, gd int) {
	j := 4*jdn + 139361631
	j += div(div(4*jdn+183187720, 146097)*3, 4)*4 - 3908
	i := div(j%1461, 4)*5 + 308
	gd = div(i%153, 5) + 1
	gm = div(i, 153)%12 + 1
	gy = div(j, 1461) - 100100 + div(8-gm, 6)
	return
}

// jalaliToJDN converts a Jalali date to a Julian Day Number.
func jalaliToJDN(jy, jm, jd int) int {
	r := jalCal(jy)
	return gregorianToJDN(r.gy, 3, r.march) + (jm-1)*31 - div(jm, 7)*(jm-7) + jd - 1
}

// ToJalali converts a Gregorian date to the Solar Hijri calendar.
func ToJalali(gy, gm, gd int) (jy, jm, jd int) {
	jdn := gregorianToJDN(gy, gm, gd)
	return jdnToJalali(jdn)
}

func jdnToJalali(jdn int) (jy, jm, jd int) {
	gy, _, _ := jdnToGregorian(jdn)
	jy = gy - 621
	r := jalCal(jy)
	jdn1f := gregorianToJDN(r.gy, 3, r.march)

	k := jdn - jdn1f
	if k >= 0 {
		if k <= 185 {
			jm = 1 + div(k, 31)
			jd = k%31 + 1
			return
		}
		k -= 186
	} else {
		jy--
		k += 179
		if r.leap == 1 {
			k++
		}
	}
	jm = 7 + div(k, 30)
	jd = k%30 + 1
	return
}

// FromJalali converts a Solar Hijri date back to Gregorian.
func FromJalali(jy, jm, jd int) (gy, gm, gd int) {
	return jdnToGregorian(jalaliToJDN(jy, jm, jd))
}
