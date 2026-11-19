package devices

// 組み込みの機種表 (仕様書 3.2)。host.deviceGuid は px4_drv の DriverHost_PX4.ini の値を
// 写すものだが、ここでは PX-W3U4 しか値が仕様書に無い。ほかの機種の GUID は
// `devices.json` か DRIVER_HOST_INI で足す。

func rx(types ...string) []string { return types }

func recv(list ...[]string) []Receiver {
	out := make([]Receiver, len(list))
	for i, t := range list {
		out[i] = Receiver{Index: i, Types: t}
	}
	return out
}

func repeat(n int, types ...string) []Receiver {
	list := make([][]string, n)
	for i := range list {
		list[i] = types
	}
	return recv(list...)
}

var px4Receivers = func() []Receiver {
	return recv(rx("BS", "CS"), rx("BS", "CS"), rx("GR"), rx("GR"))
}

// Builtin は組み込みの機種表。
func Builtin() *Defs {
	s := NewDefs()
	px4 := func(model, usb string, pair bool) {
		d := Def{
			Model: model, USB: []string{usb}, Linux: &Linux{Devname: "px4video"},
			Receivers: px4Receivers(), LNB: []int{15}, CardReader: true, PairBySerial: pair,
		}
		if model == "PLEX PX-W3U4" {
			d.Host = &Host{DeviceGUID: "{aa8de3fc-004d-49a4-b976-4250f15a976d}"}
		}
		s.Put(d)
	}
	px4("PLEX PX-W3U4", "0511:083f", false)
	px4("PLEX PX-W3PE4", "0511:023f", false)
	px4("PLEX PX-W3PE5", "0511:073f", false)
	px4("PLEX PX-Q3U4", "0511:084a", true)
	px4("PLEX PX-Q3PE4", "0511:024a", true)
	px4("PLEX PX-Q3PE5", "0511:074a", true)

	multi := []string{"GR", "BS", "CS", "CATV", "SKY"}
	s.Put(Def{Model: "PLEX PX-MLT5PE", USB: []string{"0511:024e", "0511:084e", "0511:924e"}, Linux: &Linux{Devname: "pxmlt5video"},
		Receivers: repeat(5, multi...), LNB: []int{15}, CardReader: true})
	s.Put(Def{Model: "PLEX PX-MLT8PE", USB: []string{"0511:0253"}, Linux: &Linux{Devname: "pxmlt8video"},
		Receivers: repeat(5, multi...), LNB: []int{15}, CardReader: true})
	s.Put(Def{Model: "PLEX PX-MLT8PE (3)", USB: []string{"0511:0252"}, Linux: &Linux{Devname: "pxmlt8video"},
		Receivers: repeat(3, multi...), LNB: []int{15}})
	s.Put(Def{Model: "e-Better DTV02A-4TS-P", USB: []string{"0511:0254"}, Linux: &Linux{Devname: "isdb6014video"},
		Receivers: repeat(4, multi...), LNB: []int{15}, CardReader: true})

	one := func(model, usb, devname string, cardReader bool, types ...string) {
		s.Put(Def{Model: model, USB: []string{usb}, Linux: &Linux{Devname: devname},
			Receivers: repeat(1, types...), LNB: []int{}, CardReader: cardReader})
	}
	one("DTV02-1T1S-U", "0511:004b", "isdb2056video", true, "GR", "BS", "CS")
	one("DTV02A-1T1S-U", "0511:084b", "isdb2056video", true, "GR", "BS", "CS")
	one("PLEX PX-M1UR", "0511:0854", "pxm1urvideo", true, "GR", "BS", "CS")
	one("PLEX PX-S1UR", "0511:0855", "pxs1urvideo", true, "GR")
	one("e-Better DTV03A-1TU", "0511:0052", "isdbt2071video", true, "GR")
	one("TurboSight TBS 5530", "734c:5530", "tbs5530video", false, "GR", "CATV")
	one("MyGica T230C", "0572:c68a", "px4video", false, "CATV", "SKY")
	one("Sony CECH-ZD1 J", "054c:04b2", "tornevideo", true, "GR")
	one("PIXELA PIX-DT295 Rev.01", "06b8:103b", "dt295video", true, "GR")
	one("PIXELA PIX-DT295 Rev.02", "06b8:105c", "dt295video", true, "GR")
	one("I-O DATA GV-MVP/XZ", "04bb:0530", "xzvideo", true, "GR")
	one("Siano RIO", "3275:0080", "px4video", false, "GR")
	return s
}
