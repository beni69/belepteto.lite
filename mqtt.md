# Specifikáció

Az MQTT biztosítja a backend és a terminálok (pontosabban vezérlők) kommunikációját.

A topickon az alábbi struktúrát követik:

![topics.svg](/uploads/f720f030848206078956fb05390c3c13/topics.svg)

## Controller/{id}

Minden kommunikáció a `controller/{id}` topickban történik. Ezen belül az egyes vezérlőkkel az azonosítójuknak megfelelő topicon belül történik. Az azonosító közös megegyezés alapján a szoba/ajtó száma, lehet benne szöveg is, pl. `F19C`.

Portable esetében a topic `portable/{id}` formátumú, és néhány üzenettípus más, vagy nem létezik. Ezek külön jelezve vannak a leírásban.

**Fontos:** Minden üzenetben van `timestamp`, ami vagy az esemény idejét tartalmazza, vagy `0`-t. Ha `0` van benne, akkor az üzenet beérkezésének idejét kell alapul venni. Ez alól csak a Syslog kivétel, ami mindig érvényes timestamp-et küld. A timestamp UTC időt ad meg **másodpercben**, de lehet benne tizedes utáni érték is, így megfelelően kell parse-olni.

Az ajánlott QoS mind küldő, mind fogadó oldalra értendő.

### Heartbeat

A vezérlő öt másodpercenként küldött életjele diagnosztikai adatokkal.

Topic példa: `controller/1619/heartbeat`

> Ajánlott QoS: 0
> 
> Ajánlott Expiry: 1 s

```json
{
    "uptime": 12214343244.2132323, // seconds since boot
    "ram": {
        "current": 120742, // bytes
        "minimum": 80242 // bytes
    },
    "temperature": 24.0, // ˚C
    "voltages": {
        "12v": 12.03, // V
        "5v": 4.98, // V
        "poe": 46.23, // V
        "external12ok": true // signals if external 12V connector has power
    },
    "currents": {
        "doorCurrent": 0.350, // A
        "doorMax": 0.380, // A
        "doorMin": 0.310, // A
        "system": 0.420 // A
    },
    "connection": {
        "type": "wifi", // one of ["wifi", "ethernet"]
        "link": true, // layer 2 link
        "ip": true, // layer 3 link
        "ethSpeed": "100",
        "ethDuplex": true,
        "wifiStrength": -82 // dBm
    },
    "readers": [true, false], // reader connected
    "doorOpenSinceHeartbeat": false,
    "maintenanceMode": false,
    "version": {
        "app": "1.0.3",
        "mac": "aa:bb:cc:dd:ee:ff", // MAC address of ESP (both WiFi and Ethernet use the same)
        "build": "2025-03-23T12:43:24.542Z kolos@no-potat-computing", // string that can contain any info about the build, format not specified
        "idf": "v5.5-dirty"
        "git": "033e39b-dirty" // git tag, last commit and optional dirty flag
        "boardRev": "1.2", // board revision, string, only major and minor version
    }
    "timestamp": 1755193918.547 // Unix timestamp of message
}
```

Portable esetén:

```json
{
    "uptime": 12214343244.2132323, // seconds since boot
    "ram": {
        "current": 120742, // bytes
        "minimum": 80242 // bytes
    },
    "temperature": 24.0, // ˚C
    "connection": {
        "type": "wifi", // one of ["wifi", "ethernet", "modem"]
        "link": true, // layer 2 link
        "ip": true, // layer 3 link
        "ethSpeed": "100",
        "ethDuplex": true,
        "wifiStrength": -82 // dBm
    },
    "readers": [true, false], // reader connected
    "maintenanceMode": false,
    "version": {
        "app": "1.5.1",
        "mac": "aa:bb:cc:dd:ee:ff", // MAC address of ESP (both WiFi and Ethernet use the same)
        "build": "2026-09-12T12:43:24.542Z kolos@no-potat-computing", // string that can contain any info about the build, format not specified
        "idf": "v5.5-dirty"
        "git": "033e39b-dirty" // git tag, last commit and optional dirty flag
        "boardRev": "P1.0em", // board revision, string, only major and minor version
    }
    "timestamp": 1755193918.547 // Unix timestamp of message
}
```

Verziózás [ESP dokumentáció](https://docs.espressif.com/projects/idf-component-manager/en/latest/reference/versioning.html) alapján.

### Syslog

A rendszer ezen a topickon keresztül küld naplóüzeneteket a backendnek.

Topic példa: `controller/1619/syslog`

> Ajánlott QoS: 2
> 
> Ajánlott Expiry: 120 s

```json
{
    "channel": "reader-1", // name of subsystem, custom string
    "level": "WARN", // one of ["ERROR", "WARN", "INFO", "DEBUG", "VERBOSE"]
    "message": "Disconnected",
    "timestamp": 1755193918.547
}
```

Megjegyzés: a `timestamp` nem feltétlen az üzenet beérkezésekori timestamp, mert a vezérlő tárol régebbi logokat is, amiket offline állapotban nem is próbál meg elküldeni, csak akkor, ha újra van szerver.

### Entries

A szerver nélküli kártyafeldolgozásokat és a manuális nyitásokat itt küldi le a szervernek, amikor az már elérhető. A `type` megmondja, hogy milyen jellegű esemény történt, az `accepted` pedig, hogy el lett-e fogadva a kártya. Ez csak a kártyával történő nyitásnál releváns, a nyitógombosnál fixen `true`, és a `card` értéke pedig nem definiált.

Topic példa: `controller/1619/entries`

> Ajánlott QoS: 2
> 
> Ajánlott Expiry: 120 s

```json
{
    "type": "offline", // offline/timeout/manual
    "card": "aa:bb:cc:dd:00:00:00:00",
    "accepted": true,
    "timestamp": 1755193918.547
}
```

Portable esetében nincs `manual`, de `offline` és `timeout` van.

### DoorCurrent

Ajtó nyitás után küldött adatok a nyitás alatt mért áramokról. (Min, max, avg) Csak akkor küld, ha az ajtó áramra nyílik. Áramra záró ajtó esetén a heartbeat-ben lévő adatok a mérvadóak, itt csak csupa 0 lenne küldve, ezért is nem küldi.

Topic példa: `controller/1619/doorCurrent`

> Ajánlott QoS: 0
> 
> Ajánlott Expiry: 30 s

```json
{
    "currents": {
        "doorCurrent": 0.350, // A
        "doorMax": 0.380, // A
        "doorMin": 0.310, // A
        "system": 0 // always 0, for backend compatibility
    },
    "timestamp": 1755193918.547
}
```

Portable esetében nincs.

### DoorState

Ajtó állapotának változása. (Nyitott/zárt) Csak változáskor küldi el.

Topic példa: `controller/1619/doorState`

> Ajánlott QoS: 2
> 
> Ajánlott Expiry: 120 s

```json
{
    "open": false,
    "expected": true, // door open was expected, eg. door lock was open at the time
    "timestamp": 1755193918.547
}
```

Portable esetében nincs.

### DoorCoil

Ajtómágnes állapotának változása. (Nyitott/zárt) Csak változáskor küldi el.

Topic példa: `controller/1619/doorCoil`

> Ajánlott QoS: 2
> 
> Ajánlott Expiry: 120 s

```json
{
    "open": false,
    "timestamp": 1755193918.547
}
```

Portable esetében nincs.

### CardRead

A vezérlő küldi amikor kártyaolvasás történt, rövid idejű bufferrel, az egyszerre leolvasottakat együtt. Előre adott timeout után mindet kikeresi a localDB-ből és aszerint nyit/tilt, feltételezve, hogy a szerver nem elérhető.

Topic példa: `controller/1619/cardRead`

> Ajánlott QoS: 2
> 
> Ajánlott Expiry: 1 s
>
> Ajánlott Expiry portable esetében: 5 s

```json
{
    "cards": [
        {
            "cardID": "aa:bb:cc:dd:00:00:00:00",
            "atqa": 4, // Mifare Classic 1K (0x0004)
            "sak": 8, // Mifare Classic 1K (0x08)
            "reader": [1, 2]
        },
        {
            "cardID": "aa:bb:cc:dd:ff:ff:ff:ff",
            "atqa": 68, // Mifare Plus (0x0044)
            "sak": 32, // Mifare Plus (0x20)
            "reader": [1]
        },
        ...
    ],
    "id": 118514413 // some random 64-bit number
    "timestamp": 1755193918.547
}
```

Megjegyzés: a `reader` kártyánként van, mivel az teljesen lehetséges egy kétolvasós rendszernél, hogy mindkét olvasó olvas kártyát, de akkor különböző kártyát olvastak. Az 'egy kártyát két olvasó olvasott' eset gyakorlatilag lehetetlen, de azért nem árt, ha erre is fel van készítve a fogadó oldal.

Az ID az adott kártyaolvasás egyedi azonosítója, amit az erre küldött `open` `deny`, `custom1` és `custom2` üzenetekbe is bele kell rakni. Erről bővebben a későbbi részekben.

### DBPush

A vezérlő küldi el az aktuális localDB tartalmát a szerver `dbControl` topikra küldött üzenetének hatására. Jelenleg csak a `dbControl` topicra küldött `get` üzenet hatására küld ilyet, de a backendet fel kell készíteni arra is, hogy ez nem feltétlen lesz mindig így, azaz nem szabad feltételezni, hogy ha jött egy dbPush, akkor azt megelőzte egy dbGet.

Topic példa: `controller/1619/dbPush`

> Ajánlott QoS: 0
> 
> Ajánlott Expiry: 30 s

```json
{
    "id": 635624525, // unique radom id, same across the segments of the dump
    "segmentIndex": 3,
    "segmentCount": 10,
    "db": [
        "aa:bb:cc:dd:00:00:00:00",
        "aa:bb:cc:dd:ff:ff:ff:ff",
        ...
    ],
    "timestamp": 1755193918.547
}
```

Értelemszerűen a `segmentIndex` [0, `segmentCount`[ tartományból kerül ki, és ha a szerver az összes szegmenst megkapta, csak akkor fog dbupdate-et csinálni. Ehhez az összes bejövő nem vége szegmenst (nem vége <=> `segmentIndex - 1 != segmentCount`) berakja Redis-be, és amikor vége szegmens jött, akkor a Redis-ben lévő szegmensekből (`id` alapján) megpróbálja összerakni az eredeti "üzenetet". Ha ez sikerül, akkor mehet a dbUpdate, ha nem, akkor az egészet eldobja a gyászba, és semmit nem csinál vele, pont ugyanúgy, ahogy az MQTT sem fog sérült üzenetet QOS 0 mellett kézbesíteni vagy újraküldeni, csak szimplán eldobja. És ezzel az ESP-n elég sok memóriát lehet spórolni, mert ha egy 2k méretű DB-t kell sztringként tárolni, akkor az 60-70k memóriát emészt fel, ami WiFi esetén már eléggé perem. Ezzel meg szépen blokkosítva lehet küldeni.

### DoorControl

A szerver küldi a vezérlőnek olvasás vagy távnyitás hatására. (Nyit/tilt/ignorál.)

Amennyiben kártyaolvasás történt, és akármilyen DoorControl üzenet jön, akkor a nem történik localDB lookup.

Topic példa: `controller/1619/doorControl`

> Ajánlott QoS: 2
> 
> Ajánlott Expiry: 1 s
>
> Ajánlott Expiry portable esetében: 5 s

Portable esetében is létezik, viszont ott csak a felhasználói visszajelzést vezérli.

Parancsok:

- `open`: kinyitja az ajtót egy előre meghatározott ideig, majd automatikusan visszazárja azt. CardRead vagy manuális nyitás reakciójaként ajánlott használni.
- `open <id>`: ld. `open`, a kártyaolvasásra küldött válaszban kell küldeni, ahol az `<id>` a kártyaolvasáshoz küldött ID, pl. `open 118514413`.
- `deny`: visszajelzés a felhasználónak, hogy meg lett tagadva a kártyaolvasás.
- `deny <id>`: ld. open.
- `custom1`: egyedi visszajelzés a felhasználónak.
- `custom1 <id>`: ld. open.
- `custom2`: egyedi visszajelzés a felhasználónak.
- `custom2 <id>`: ld. open.
- `ignore`: csak az előbb említett localDB lookup megakadályozása. Kártya felhasználóhoz rendeléshez ajánlott a használata. Pl. a felhasználó párósítási módba rak egy vezérlőt, és akkor ha lehúzza a kártyáját, akkor nem beengedi vagy visszautasítja, hanem semmit nem csinál, mert nem bemenni akart a felhasználó, hanem csak kártyát önmagához rendelni. Ilyenkor a szerver küld egy ignore` üzenetet, és a vezérlő sem fogja localDB-ből kikeresni a kártyát.

Megjegyzés: a parancsok _plaintext_-ként kell küldeni, semmilyen JSON vagy egyéb csomagolás nem kell, és a vezérlő nem is támogatja.

Megjegyzés: hibás parancs esetén arra nem reagál, hibaüzenet nem képződik.

Megjegyzés: az ID-vel ellátott üzenetek használata opcionális, de erősen ajánlott, mivel hálózati/feldolgozási késleltetések esetén nem lesz dupla ajtónyitás/visszajelzés.

### Command

A szerver küld a vezérlőnek parancsokat.

Topic példa: `controller/1619/command`

> Ajánlott QoS: 2
> 
> Ajánlott Expiry: 1 s
> 
> Ajánlott Expiry nem időkritikus parancsoknál (`alternativeColorOn` és `alternativeColorOff` kivételével az összes): 30 s

Parancsok:

 - `alternativeColorOn`: Alternatív üresjárati szín aktiválása, kártya hozzárendeléshez ajánlott a használata, hogy a felhasználónk jelezzük, hogy a vezérlő kártyára vár. Kártyaolvasáskor automatikusan törlődik. Az alternatív szín semmilyen módon nem befolyásolja a vezérlő működését, csak a leolvasó színét módosítja, kártyaolvasás és minden egyéb ugyanúgy működik.
 - `alternativeColorOff`: Alternatív szín parancs törlése. Ajánlott használata manuálos kártyaolvasás timeout esetében.
 - `checkUpdates`: Frissítések keresése OTA-n keresztül. A vezérlő mindenképpen újra fog indulni.
 - `checkPartitionUpdates <timestamp>`: Partíciós tábla frissítése OTA-n keresztül. A timestamp a pontos idő +/- 60 másodperc kell, hogy legyen, különben nem fog semmit sem csinálni. A vezérlő mindenképpen újra fog indulni. Timestamp másodpercben értendő.
 - `reboot`: Újraindítja a vezérlőt.
 - `maintenanceModeOn`: Minden külső felhasználói interakciót letilt, csak a szerver parancsaira és az USB-s konzolra fog reagálni. A _maintenance mode_ újraindítás után is megmarad. Csak a `maintenanceModeOff` paranccsal vagy az USB-s konzolon keresztül lehet kikapcsolni.
 - `maintenanceModeOff`: _Maintenance mode_ kikapcsolása.

Megjegyzés: a parancsok _plaintext_-ként kell küldeni, semmilyen JSON vagy egyéb csomagolás nem kell, és a vezérlő nem is támogatja.

Megjegyzés: hibás parancs esetén arra nem reagál, hibaüzenet nem képződik.

### DBControl

A szerver küldi ha változást akar a vezérlő tárolt kártyalistáján.

Topic példa: `controller/1619/dbControl`

> Ajánlott QoS `get` esetén: 2
>
> Ajánlott QoS egyéb esetben: 1
> 
> Ajánlott Expiry: 30 s

Parancsok:

- `get`: elindítja a localDB szerverre küldését.
- `add aa:bb:cc:dd:00:00:00:00`: hozzáadja a localDB-hez az adott azonosítójú kártyát. Ha már hozzá van adva, egy warning üzenet fog generálódni, de ugyanúgy benne marad a DB-ben a kártya.
- `delete aa:bb:cc:dd:ff:ff:ff:ff`: eltávolitja a localDB-ből az adott azonosítójú kártyát. Ha már nincs benne, akkor sem lesz belőle hiba.

Megjegyzés: a kártya azonosítókat mindenképpen kettőspontokkal kell elválasztani, és a végén meg kell lennie a kellő mennyiségű `00`-nak, ha a kártya csak kevesebb bitet tartalmaz. A hexa digit-et lehetnek kis- és nagybetűsek is.

### Healthcheck

A szerver küldi periodikusan, hogy él és virul, hogy a vezérlő tudja, hogy minden rendben van vele. A szerver adott időközönként küld üzenetet, jelezve az állapotát. Ha a vezérlő előre megadott ideig nem kap üzenetet, akkor automatikusan offline-nak fogja venni a szervert.

**FONTOS:** Nem a megszokott `controller/<id>/...` topic-on megy a kommunikáció, hanem a `backend/healthcheck`-en!

Topic: `backend/healthcheck`

> Ajánlott QoS: 1
> 
> Ajánlott Expiry: 30 s

Parancsok:

- `true`: a szerver kártyafeldolgozásra képes, a kártyaolvasás üzenetre megfelelő választ fog adni
- `false`: a szerver nem képes kártyafeldolgozásra
