BSV20
deploy

## Token
---------
|Collection        |Key                         |Type   |Field/Score            |Value        
|-|-|-|-|-
|**General Purpose**
|Indexer Progress  |progress                    |HASH   |indexer                |height
|Indexer Log       |idx:log:`indexer`           |STREAM |height-idx             |txid
|Tx Log            |tx:log                      |SSET   |height/unix            |txid
|TXOs              |txo:`outpoint`              |JSON   |                       |Txo
|TXIs              |txi:`txid`                  |SSET   |vin                    |outpoint   
|Txo State         |txo:state                   |SSET   |spent.height/unix      |
|Txo Tags          |tag:`tag`                   |SSET   |spent.height/unix      |outpoint
|Address Sync      |add:sync                    |HASH   |address                |syncHeight
||
|**General Purpose Index**
|GP Output Index   |oi:`tag`:`idxName`          |SSET   |spent.height/unix      |fieldPath=value:outpoint
|GP Score Index    |si:`tag`:`idxName`          |SSET   |score                  |value
|GP Log            |el:`tag`:`logName`          |STREAM |height-idx             |map
||
|**Fungibles**
|Token             |f:token:tickId              |JSON   |                       |Token
|Token Supply      |f:supply                    |SSET   |supply                 |tickId
|Validate          |f:validate:`tickId`:`height`|SSET   |idx                    |outpoint
|FungTxos          |oi:bsv20:`tickId`           |SSET   |spent.height/unix      |outpoint
|FungAddressTxos   |oi:bsv20:a:`add`:`tickId`   |SSET   |spent.height/unix      |outpoint
|Holders           |si:bsv20:hold:`tickId`      |SSET   |balance                |address
|Listings          |si:bsv20:list:`tickId`      |SSET   |status.ppt             |outpoint
|Token Status      |si:bsv20:stat:`tickId`      |SSET   |status                 |outpoint
|Address Market    |el:bsv20:mka:`address`      |STREAM |height-idx             |[listing, sale, cancel]
|TickId Market     |el:bsv20:mkt:`tickId`       |STREAM |height-idx             |[listing, sale, cancel]
|BSV20 Legacy      |bsv20Legacy             
|**Funding**
|Funds             |f:fund:`tickId`             |JSON   |tickId                 |TokenFund
|Fund Total        |f:fund:total                |SSET   |fundTotal              |tickId
|Fund Balance      |f:fund:bal                  |SSET   |fundBal                |tickId
||
|**Ordinals**
|Inscription Seq   |si:insc:seq                 |SSET   |height/unix.idx       |outpoint
|Origin            |oi:origin:`origin`          |SSET   |spent.height/unix     |outpoint 
||
|**Cache**
|Holders Calculted |FHOLDCACHE                  |HASH   |tick                   |unix

