package manager

import "github.com/warthog618/sms/encoding/tpdu"

func trimDeliverTPDUToDeclaredLength(tpduBytes []byte) ([]byte, bool) {
	want, ok := deliverTPDUDeclaredLength(tpduBytes)
	if !ok || want >= len(tpduBytes) {
		return tpduBytes, false
	}
	return append([]byte(nil), tpduBytes[:want]...), true
}

func deliverTPDUDeclaredLength(tpduBytes []byte) (int, bool) {
	if len(tpduBytes) < 1 || tpduBytes[0]&0x03 != 0 {
		return 0, false
	}

	i := 1
	if i+2 > len(tpduBytes) {
		return 0, false
	}
	oaLen := int(tpduBytes[i])
	i += 2 // OA length + TOA
	oaOctets := (oaLen + 1) / 2
	if i+oaOctets > len(tpduBytes) {
		return 0, false
	}
	i += oaOctets

	if i+10 > len(tpduBytes) {
		return 0, false
	}
	dcs := tpduBytes[i+1]
	i += 2 + 7
	udl := int(tpduBytes[i])
	i++

	alphabet, err := tpdu.DCS(dcs).Alphabet()
	if err != nil {
		return 0, false
	}

	udOctets := udl
	if alphabet == tpdu.Alpha7Bit {
		udOctets = (udl*7 + 7) / 8
	}
	want := i + udOctets
	if want > len(tpduBytes) {
		return 0, false
	}
	return want, true
}
