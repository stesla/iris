package telnet

const (
	// RFC 885
	EOR = 239 + iota // ef
	// RFC 854
	SE   // f0
	NOP  // f1
	DM   // f2
	BRK  // f3
	IP   // f4
	AO   // f5
	AYT  // f6
	EC   // f7
	EL   // f8
	GA   // f9
	SB   // fa
	WILL // fb
	WONT // fc
	DO   // fd
	DONT // fe
	IAC  // ff
)

const (
	TransmitBinary  = 0  // RFC 856
	Echo            = 1  // RFC 857
	SuppressGoAhead = 3  // RFC 858
	Charset         = 42 // RFC 2066
	TerminalType    = 24 // RFC 930
	NAWS            = 31 // RFC 1073
	EndOfRecord     = 25 // RFC 885
)

const (
	CharsetRequest = 1 + iota
	CharsetAccepted
	CharsetRejected
	CharsetTTableIs
	CharsetTTableRejected
	CharsetTTableAck
	CharsetTTableNak
)
