package handler

import (
	"GoDrive/aws"
	"GoDrive/cache"
	"GoDrive/config"
	"GoDrive/meta"
	"GoDrive/timer"
	"GoDrive/utils"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/garyburd/redigo/redis"
	"github.com/gin-gonic/gin"
)

func exist(dir string) bool {
	_, err := os.Stat(dir) //os.Stat获取文件信息
	if err != nil {
		if os.IsExist(err) {
			return true
		}
		return false
	}
	return true
}

func getFileDir(filehash string) string {
	chunkRootPath := config.ChunkFileStoreDirectory
	dir := chunkRootPath + filehash + "/"

	return dir
}

// return true and the current index list if the file exist in tmp local, false if not
func getLocalChunks(filehash string) (bool, []int) {
	dir := getFileDir(filehash)
	if ex := exist(dir); !ex {
		return false, make([]int, 0)
	}

	files, err := ioutil.ReadDir(dir)
	if err != nil {
		panic(err)
	}

	var indexList []int
	for _, f := range files {
		idx, _ := strconv.Atoi(strings.Split(f.Name(), "_")[1])
		log.Println(idx)
		indexList = append(indexList, idx)
	}

	log.Printf("%v\n", indexList)
	return true, indexList
}

// get AWS uploadId from redis
func getAWSUploadId(filehash string) (string, error) {
	rConn := cache.ChunkPool().Get()
	defer rConn.Close()
	uploadId, err := redis.String(rConn.Do("HGET", "aws", filehash))
	return uploadId, err
}

// GetPrevChunks : init before the actual upload
func GetPrevChunks(c *gin.Context) {
	username, exist := c.Get("username")
	if !exist {
		fmt.Println("Get username from context failed")
		c.JSON(200, gin.H{
			"code": 1,
			"msg":  "Auth failed",
		})
		return
	}
	filehash := c.Query("filehash")
	fileName := c.Query("filename")
	log.Println(username)

	if config.StoreMethod == "AWS" {
		rConn := cache.ChunkPool().Get()
		defer rConn.Close()
		uploadId, err := redis.String(rConn.Do("HGET", "aws", filehash))
		if err != nil {
			if err == redis.ErrNil {
				// no uploadId yet, init the upload process
				newUploadId := aws.InitAWSMpUpload(filehash, fileName)
				// set the AWS uploadId to redis
				rConn.Do("HSET", "aws", filehash, newUploadId)
				c.JSON(200, gin.H{
					"code": 0,
					"msg":  "No current chunks",
					"data": gin.H{
						"uploadedList": make([]int, 0),
					},
				})
				return
			} else {
				panic(err.Error())
			}
		}
		// uploadId exists, resume uploading
		log.Printf("uploadId: %s\n", uploadId)
		uploadedIdxList := aws.GetPartList(filehash, uploadId)
		fmt.Printf("aws uploadId: %s\n", uploadId)

		c.JSON(200, gin.H{
			"code": 0,
			"msg":  "Previous chunk detected",
			"data": gin.H{
				"uploadedList": uploadedIdxList,
			},
		})
	} else {
		dirExist, uploadedIdxList := getLocalChunks(filehash)
		// Case 1: no previous chunks
		if !dirExist {
			os.MkdirAll(getFileDir(filehash), 0744)
			c.JSON(200, gin.H{
				"code": 0,
				"msg":  "No current chunks",
				"data": gin.H{
					"uploadedList": uploadedIdxList,
				},
			})
			return
		}
		// Case 2: have current chunk
		c.JSON(200, gin.H{
			"code": 0,
			"msg":  "Previous chunks detected",
			"data": gin.H{
				"uploadedList": uploadedIdxList,
			},
		})
	}
}

// GetFileChunk receives the file chunk
func GetFileChunk(c *gin.Context) {
	username, exist := c.Get("username")
	if !exist {
		fmt.Println("Get username from context failed")
		c.JSON(200, gin.H{
			"code": 1,
			"msg":  "Auth failed",
		})
		return
	}

	chunk, err := c.FormFile("chunk")
	if err != nil {
		fmt.Println(err.Error())
		c.JSON(200, gin.H{
			"code": 1,
			"msg":  "server parse chunk file failed",
		})
		return
	}
	uploadID := c.PostForm("uploadId")
	chunkID := c.PostForm("chunkId")
	filename := c.PostForm("filename")
	filehash := c.PostForm("filehash")
	chunkIdx := c.PostForm("index")

	fileuser := strings.Split(uploadID, "-")[0]
	log.Println("current user's username", fileuser)

	if username != fileuser {
		log.Println("Authentication error, uploadId belonger is not current user")
		return
	}
	log.Printf("filename : %s\nuploadId: %s, chunkid: %s\n", filename, uploadID, chunkID)

	if config.StoreMethod == "AWS" {
		// get aws upload id
		rConn := cache.ChunkPool().Get()
		defer rConn.Close()

		awsUploadId, err := redis.String(rConn.Do("HGET", "aws", filehash))
		if err != nil {
			panic(err)
		}
		// upload to aws
		fd, err := chunk.Open()
		if err != nil {
			panic(err)
		}
		idx, _ := strconv.Atoi(chunkIdx)
		aws.UploadChunkToAws(fd, filehash, int64(idx+1), awsUploadId)

		c.JSON(200, gin.H{
			"code": 0,
			"msg":  chunkID + " upload suc to AWS",
		})
	} else {
		// store the file locally
		tempPath := path.Join(getFileDir(filehash), chunkID)
		if err := c.SaveUploadedFile(chunk, tempPath); err != nil {
			c.String(http.StatusBadRequest, "failed to save chunk")
			return
		}

		c.JSON(200, gin.H{
			"code": 0,
			"msg":  chunkID + " upload suc",
		})
	}

	return
}

// CheckIntegrity checks the file hash again to make sure the file is not modified
func CheckIntegrity(c *gin.Context) {
	type body struct {
		Filehash    interface{} `json:"filehash"`
		Filename    string      `json:"filename"`
		ChunkLength int         `json:"chunkLength"`
		Filesize    int64       `json:"filesize"`
	}

	username, _ := c.Get("username")
	var b body
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(200, gin.H{
			"code": 1,
			"msg":  err.Error(),
		})
		panic(err)
	}

	fileHash := fmt.Sprintf("%v", b.Filehash)
	log.Printf("Checking integrity.. Filename: %s, Filehash: %v", b.Filename, b.Filehash)

	if config.StoreMethod == "AWS" {
		awsUploadId, err := getAWSUploadId(fileHash)
		if err != nil {
			panic(err)
		}
		aws.CompleteAWSPartUpload(fileHash, awsUploadId)
		// delete uploadId key from redis
		rConn := cache.ChunkPool().Get()
		defer rConn.Close()
		_, err = rConn.Do("HDEL", "aws", fileHash)
		if err != nil {
			panic(err)
		} else {
			log.Println("UploadId key deleted from aws redis")
		}
		fileMeta := meta.FileMeta{
			FileName: b.Filename,
			FileMD5:  fileHash,
			FileSize: b.Filesize,
			Location: "aws",
			UploadAt: time.Now().Format("2006-01-02 15:04:05"),
			IsSmall:  false,
		}
		meta.UpdateFileMetaDB(fileMeta, username.(string))

		timer.Elapse = time.Since(timer.StartTime)
		fmt.Println("ELAPSE TIME: ", timer.Elapse)

	} else {
		mdhash := new(utils.MD5Stream)
		folder := config.ChunkFileStoreDirectory + fileHash + "/"
		counter := 0

		filepath.Walk(folder, func(path string, info os.FileInfo, err error) error {
			if !info.IsDir() {
				counter++
			}

			return nil
		})
		// missing chunks
		if counter != b.ChunkLength {
			c.JSON(200, gin.H{
				"code": 1,
				"msg":  "Missing chunks",
			})
			return
		}
		// iterate files
		var chunks sortedChunk
		chunks, _ = ioutil.ReadDir(folder)
		log.Printf("chunk count: %d\n", len(chunks))

		// sort chunks based on name
		sort.Sort(chunks)
		for _, v := range chunks {
			chunkContent, err := ioutil.ReadFile(folder + "/" + v.Name())
			if err != nil {
				panic(err)
			}
			mdhash.Update(chunkContent)
		}
		hash := mdhash.Sum()

		// panic("123123"6
		if hash != fileHash {
			c.JSON(200, gin.H{
				"code": 1,
				"msg":  "server file integrity checking failed! Please try to reupload",
			})
			return
		}
		log.Printf("hash after server calculation is: %s\n", hash)

		// save meta data to db
		fileMeta := meta.FileMeta{
			FileName: b.Filename,
			FileMD5:  fileHash,
			FileSize: b.Filesize,
			Location: folder,
			UploadAt: time.Now().Format("2006-01-02 15:04:05"),
			IsSmall:  false,
		}
		meta.UpdateFileMetaDB(fileMeta, username.(string))

		c.JSON(200, gin.H{
			"code": 0,
			"msg":  "File uploaded successfully",
		})
	}
	return
}

type sortedChunk []os.FileInfo

/**
Comparator interface for SortedChunk
*/
func (a sortedChunk) Len() int {
	return len(a)
}

func (a sortedChunk) Swap(i, j int) {
	a[i], a[j] = a[j], a[i]
}

func (a sortedChunk) Less(i, j int) bool {
	idx1, _ := strconv.Atoi(strings.Split(a[i].Name(), "_")[1])
	idx2, _ := strconv.Atoi(strings.Split(a[j].Name(), "_")[1])

	return idx1 < idx2
}
