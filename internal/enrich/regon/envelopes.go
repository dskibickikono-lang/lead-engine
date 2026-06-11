package regon

// SOAP 1.2 envelopes for BIR1.1 (UslugaBIRzewnPubl). %s placeholders are
// filled with fmt.Sprintf; the To header is required by the service.
const (
	envZaloguj = `<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" xmlns:ns="http://CIS/BIR/PUBL/2014/07" xmlns:wsa="http://www.w3.org/2005/08/addressing">
<soap:Header><wsa:To>%s</wsa:To><wsa:Action>http://CIS/BIR/PUBL/2014/07/IUslugaBIRzewnPubl/Zaloguj</wsa:Action></soap:Header>
<soap:Body><ns:Zaloguj><ns:pKluczUzytkownika>%s</ns:pKluczUzytkownika></ns:Zaloguj></soap:Body>
</soap:Envelope>`

	envSzukaj = `<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" xmlns:ns="http://CIS/BIR/PUBL/2014/07" xmlns:dat="http://CIS/BIR/PUBL/2014/07/DataContract" xmlns:wsa="http://www.w3.org/2005/08/addressing">
<soap:Header><wsa:To>%s</wsa:To><wsa:Action>http://CIS/BIR/PUBL/2014/07/IUslugaBIRzewnPubl/DaneSzukajPodmioty</wsa:Action></soap:Header>
<soap:Body><ns:DaneSzukajPodmioty><ns:pParametryWyszukiwania><dat:Nip>%s</dat:Nip></ns:pParametryWyszukiwania></ns:DaneSzukajPodmioty></soap:Body>
</soap:Envelope>`

	envRaport = `<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" xmlns:ns="http://CIS/BIR/PUBL/2014/07" xmlns:wsa="http://www.w3.org/2005/08/addressing">
<soap:Header><wsa:To>%s</wsa:To><wsa:Action>http://CIS/BIR/PUBL/2014/07/IUslugaBIRzewnPubl/DanePobierzPelnyRaport</wsa:Action></soap:Header>
<soap:Body><ns:DanePobierzPelnyRaport><ns:pRegon>%s</ns:pRegon><ns:pNazwaRaportu>%s</ns:pNazwaRaportu></ns:DanePobierzPelnyRaport></soap:Body>
</soap:Envelope>`

	envWyloguj = `<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" xmlns:ns="http://CIS/BIR/PUBL/2014/07" xmlns:wsa="http://www.w3.org/2005/08/addressing">
<soap:Header><wsa:To>%s</wsa:To><wsa:Action>http://CIS/BIR/PUBL/2014/07/IUslugaBIRzewnPubl/Wyloguj</wsa:Action></soap:Header>
<soap:Body><ns:Wyloguj><ns:pIdentyfikatorSesji>%s</ns:pIdentyfikatorSesji></ns:Wyloguj></soap:Body>
</soap:Envelope>`
)
