package qsc

import (
	"github.com/QOSGroup/qbase/context"
	"github.com/tendermint/tendermint/crypto"
)

type GenesisState struct {
	RootPubKey crypto.PubKey `json:'ca_root_pub'`
}

func NewGenesisState(pubKey crypto.PubKey) GenesisState {
	return GenesisState{
		pubKey,
	}
}

func InitGenesis(ctx context.Context, data GenesisState) {
	ctx.Mapper(QSCMapperName).(*QSCMapper).SetQSCRootCA(data.RootPubKey)
}