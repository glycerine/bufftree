# Previous unlocked README tables

Preserved from README.md at commit `028a3b414702e652d1607e1c5a50fbeee7e4d3b4`, before the transaction API.
These are historical reported values, not a rerun of the old implementation.

| Operation (showing ns/key) | BPtree   | builtin Go map | tidwall/btree | red-black tree |
| -------------------------- | -------: | -------------: | ------------: | -------------: |
| Get                        |    101.4 |           16.5 |         116.7 |          199.7 |
| Put                        |    212.6 |          168.8 |         319.1 |          583.4 |
| Ordered scan               |     3.13 |  not supported |          4.17 |          16.11 |

| Operation (showing ns/key)    |   BPtree | builtin Go map | tidwall/btree | red-black tree |
| ----------------------------- | -------: | -------------: | ------------: | -------------: |
| Tree `Get`, hit               |    101.4 |           16.5 |         116.7 |          199.7 |
| Tree `Get`, miss              |     98.4 |           16.1 |         118.4 |          221.4 |
| Tree `Put`, existing key      |     97.2 |           27.1 |         126.9 |          207.0 |
| Tree `Put`, fresh key         |    212.6 |          168.8 |         319.1 |          583.4 |
| Ordered scan, maximum 10,000  |     3.11 |  not supported |          4.13 |          15.80 |
| Ordered scan, maximum 100,000 |     3.13 |  not supported |          4.17 |          16.11 |
