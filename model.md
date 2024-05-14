BSV20
deploy

## Token
---------
|Collection        |Key                     |Type |Field/Score            |Value        
|-|-|-|-|-
|**General**
|Indexer Progress  |PROGRESS                |HASH |indexer                |height
|Transactions      |TXLOG                   |SSET |height                 |txid
|Txo State         |TXOSTATE                |SSET |spent.height|unix
|**Fungibles**
|Tokens            |FUNGIBLE:tickId         |JSON |                       |Fungible
|TXOs              |FTXO:outpoint           |JSON |                       |FungibleTxo
|Token outpoints   |FTXOSTATE:tickId        |SSET |spent.height|unix      |outpoint
|Validate          |FVALIDATE:tickId:height |SSET |idx                    |outpoint
|Token Supply      |FSUPPLY                 |SSET |supply                 |tickId
|Token Spend       |FTXI:txid:tickId        |SET  |                       |outpoint   
<!-- |Token Status      |FSTATUS                 |SSET |status                 |outpoint -->
|**Addresses**
|Address outpoints |FADDTXO:address:tickId  |SSET |spent.height|unix      |outpoint
|Address spends    |FADDSPND:address:tickId |SSET |spend_height.idx       |outpoint
|**Market**
|Listings          |FLIST:tickId            |SSET |ppt                    |outpoint
|Sales             |FSALE:tickId            |SSET |height.idx             |outpoint
|**Funding**
<!-- |Fund Total        |FUNDTOTAL               |SSET |fundTotal              |tickId
|Fund Used         |FUNDUSED                |SSET |fundUsed               |tickId -->
|Fund Balance      |FUNDBAL                 |SSET |fundBal                |tickId
|Funds             |FUND                    |HSET |tickId                 |TokenFund
|**Stats**
|Holders           |FHOLD:tickId            |SSET |balance                |address


|**Cache**
|Binary TXO        |TXOBIN                  |HASH |spent.height|unix      |output


Status
