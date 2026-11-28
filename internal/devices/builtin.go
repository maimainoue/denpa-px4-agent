package devices

// 組み込みの機種表 (仕様書 3.2)。host.deviceGuid は px4_drv (fork) の DriverHost_PX4.ini の値を写す。
// PX-W3U4・Sony CECH-ZD1 J・I-O DATA GV-MVP/XZ は実機で確かめた値、そのほかは ini の値をそのまま写したもの。
// ini にあって、ここに無い機種 (PX-W3U4 以外の PLEX など) の GUID は `devices.json` か DRIVER_HOST_INI で足す。

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

func intPtr(v int) *int { return &v }

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
			d.Host = &Host{DeviceGUID: "{aa8de3fc-004d-49a4-b976-4250f15a976d}", InterfaceGUID: "{6d78c843-49d8-4bb8-8dfa-286fdee4c8a3}"}
		}
		s.Put(d)
	}
	px4("PLEX PX-W3U4", "0511:083f", false)
	px4("PLEX PX-W3PE4", "0511:023f", false)
	px4("PLEX PX-W3PE5", "0511:073f", false)
	px4("PLEX PX-Q3U4", "0511:084a", true)
	px4("PLEX PX-Q3PE4", "0511:024a", true)
	px4("PLEX PX-Q3PE5", "0511:074a", true)

	pxmlt := func(model, devname string, n int, cardReader bool, usb ...string) {
		s.Put(Def{Model: model, USB: usb, Linux: &Linux{Devname: devname},
			Receivers: repeat(n, "GR", "BS", "CS", "CATV", "SKY"), LNB: []int{15}, CardReader: cardReader})
	}
	pxmlt("PLEX PX-MLT5PE", "pxmlt5video", 5, true, "0511:024e", "0511:084e", "0511:924e")
	pxmlt("PLEX PX-MLT8PE", "pxmlt8video", 5, true, "0511:0253")
	pxmlt("PLEX PX-MLT8PE (3)", "pxmlt8video", 3, false, "0511:0252")
	pxmlt("e-Better DTV02A-4TS-P", "isdb6014video", 4, true, "0511:0254")

	one := func(model, usb, devname string, cardReader bool, types ...string) {
		s.Put(Def{Model: model, USB: []string{usb}, Linux: &Linux{Devname: devname},
			Receivers: repeat(1, types...), LNB: []int{}, CardReader: cardReader})
	}
	one("DTV02-1T1S-U", "0511:004b", "isdb2056video", true, "GR", "BS", "CS")
	one("DTV02A-1T1S-U", "0511:084b", "isdb2056video", true, "GR", "BS", "CS")
	one("PLEX PX-M1UR", "0511:0854", "pxm1urvideo", true, "GR", "BS", "CS")
	one("PLEX PX-S1UR", "0511:0855", "pxs1urvideo", true, "GR")
	one("e-Better DTV03A-1TU", "0511:0052", "isdbt2071video", true, "GR")

	one("Siano RIO", "3275:0080", "px4video", false, "GR")
	one("Sony CECH-ZD1 J", "054c:04b2", "tornevideo", true, "GR")
	one("MyGica T230C", "0572:c68a", "px4video", false, "CATV", "SKY")

	one("MyGica T230A", "0572:689a", "px4video", false, "CATV")
	one("PIXELA PIX-DT295 Rev.01", "06b8:103b", "dt295video", true, "GR")
	one("PIXELA PIX-DT295 Rev.02", "06b8:105c", "dt295video", true, "GR")
	one("TurboSight TBS 5530", "734c:5530", "tbs5530video", false, "GR", "CATV")
	one("I-O DATA GV-MVP/XZ", "04bb:0530", "xzvideo", true, "GR")

	// HZ2W は内蔵カード (B-CAS) だけで、受信機は無い。起動前は 1738:5211 (UT100B と共通)。
	s.Put(Def{Model: "I-O DATA GV-MVP/HZ2W", USB: []string{"04bb:0526"}, CardReader: true})
	// UT100B は、Linux では chardev ではなく libusb 版のホストで動くので、linux の定義は無い。
	s.Put(Def{Model: "Quixun QRS-UT100B", USB: []string{"3275:7020"},
		Receivers: repeat(1, "GR"), LNB: []int{}, CardReader: true})

	// Windows と macOS の DriverHost_PX4 の機種の GUID (px4_drv の DriverHost_PX4.ini の [DeviceDefinitionN] の GUID)。
	// iface は DeviceInterfaceGUID (WinUSB の機材を数える鍵)。
	host := func(model, guid, iface string) {
		d, ok := s.Get(model)
		if !ok {
			panic("builtin: 機種表に無い機種に GUID を付けようとした: " + model) // 書き損じ (機種の定義を先に置く)
		}
		d.Host = &Host{DeviceGUID: guid, InterfaceGUID: iface}
	}
	host("Siano RIO", "{9DFE7ABB-8BAA-4004-9AF2-08AE5F4309B0}", "{46B7CEBB-B23F-4F4D-9C99-680F37950977}")       // [DeviceDefinition16] PLEX PX-S1UD / PX-Q1UD
	host("Sony CECH-ZD1 J", "{D3C0AEC3-0FC9-4769-8513-8323CEFD792F}", "{38E7A8C5-27D1-440D-9BCF-83E1B20FB92F}") // [DeviceDefinition18] (受信機 0 の GUID は {F41EF4C4-31A6-4044-9707-DE9DA0AF7A86})

	host("MyGica T230A", "{08546a8a-6781-4235-b778-0acdeb9ffb16}", "{51d80ba0-8b6f-4d2d-9cd6-64a1dc28efe2}")            // [DeviceDefinition19]
	host("PIXELA PIX-DT295 Rev.01", "{F9292CE5-229D-4247-8B4C-1A5B4E250C71}", "{BA834A78-739B-40E7-A1BC-B63920975BDF}") // [DeviceDefinition20]
	host("PIXELA PIX-DT295 Rev.02", "{944EE4B3-CC76-40D8-8AB6-6F33B1C6D983}", "{82C204EA-A858-4EEA-A58B-4B26FA9893BE}") // [DeviceDefinition21]
	host("TurboSight TBS 5530", "{3C0B7B52-6A59-4E83-9B0E-5E5D8C0D4A21}", "{7A1D4E96-2B38-4C5F-8F61-0D9A3E7B5C42}")     // [DeviceDefinition23]
	host("I-O DATA GV-MVP/XZ", "{9D54F32C-9BD4-4758-B0FA-3B8050CB29E0}", "{EFD1A173-B5DA-4BBC-8AC1-B2318799EC9E}")      // [DeviceDefinition22] (受信機 0 の GUID は {A3C846A3-9E77-4631-BCF7-91CBBA3E54D1})
	host("I-O DATA GV-MVP/HZ2W", "{492958E6-3D24-4895-8B1E-082A3446690F}", "{CD6AE7C3-5A4D-42A4-B33F-F0F0A6CFAEED}")    // [DeviceDefinition25] (24 は firmware loader で、対象外)
	host("Quixun QRS-UT100B", "{A96B39EB-E5A6-4901-A859-BD974021F1F2}", "{B1DFEDB0-D90F-470A-B5D0-776750BF954F}")       // [DeviceDefinition26]

	// Sony nasne (HTTP) は仕様書 6.5 の置き方 A。devices.json を書かなくても tuners.json だけで使える。
	// エージェントが HTTP の GET で直接読む (curl は使わない)。503 は使用中 (409)、404 は同期しなかった、
	// それ以外のステータスは、使用中でない失敗として理由を伝える (#17)。
	// {device} は tuners.json の device の末尾 (ホスト名か IPv4)。
	s.Put(Def{Model: "Sony nasne (nasne-recpt1 HTTP)",
		URL:           "http://{device}:8301/tuner/{channel}{sidQuery}",
		ChannelFormat: FormatRecpt1,
		SingleService: true, DecryptedByDevice: true,
		Receivers: recv(rx("GR", "BS", "CS")),
		LNB:       []int{},
	})
	// 同じ nasne を、B-CAS を使わずに読む (nasne-recpt1 の ?bcas=0。仕様書 6.5)。nasne は B-CAS を見ず、
	// 復号しない生の TS (全 PID) を出す。復号はエージェントが、自分のカード (か CARD_URL で貰う鍵) で行う。
	// なので decryptedByDevice は false、生の TS は全部の局を含むので singleService も false
	// (同じチャンネルの別の局は、同じ選局に相乗りできる)。sid は nasne が無視するので URL に付けない。
	s.Put(Def{Model: "Sony nasne (nasne-recpt1 HTTP raw)",
		URL:           "http://{device}:8301/tuner/{channel}?bcas=0",
		ChannelFormat: FormatRecpt1,
		Receivers:     recv(rx("GR", "BS", "CS")),
		LNB:           []int{},
	})
	markDenpa(s)
	return s
}
