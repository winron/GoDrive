package handler

import (
	"GoDrive/aws"
	"GoDrive/config"
	"GoDrive/db"
	"GoDrive/meta"
	"GoDrive/timer"
	"GoDrive/utils"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const goos string = runtime.GOOS

// UploadHandler handles file upload
func UploadHandler(c *gin.Context) {
	head, err := c.FormFile("file")
	clientHash := c.PostForm("filehash")

	if err != nil {
		panic(err.Error())
	}

	var basepath string = config.WholeFileStoreLocation + head.Filename
	fileMeta := meta.FileMeta{
		FileName: head.Filename,
		UploadAt: time.Now().Format("2006-01-02 15:04:05"),
		IsSmall:  true,
	}

	err = c.SaveUploadedFile(head, basepath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":  1,
			"msg":   "Internal Server Error: Failed to save file to the DB.",
			"error": err.Error(),
		})
		return
	}

	newFile, err := os.Open(basepath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":  1,
			"msg":   "Internal Server Error: Failed to save file to the DB.",
			"error": err.Error(),
		})
		return
	}

	defer newFile.Close()
	newFileInfo, err := newFile.Stat()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":  1,
			"msg":   "Internal Server Error: Failed to save file to the DB.",
			"error": err.Error(),
		})
		return
	}
	// update file meta hashmap
	fileMeta.Location = "aws"
	fileMeta.FileSize = newFileInfo.Size()
	fileMeta.FileMD5 = utils.FileMD5(newFile)

	// integrity checking
	if fileMeta.FileMD5 != clientHash {
		// integrity checking failed
		c.JSON(200, gin.H{
			"code": 1,
			"msg":  "Server integrity check failed, please try upload again later",
		})
		return
	}

	// getting username
	username, exist := c.Get("username")
	if !exist {
		fmt.Printf("Failed to find username.")
	}

	// upload meta data to databases
	uploadDB := meta.UpdateFileMetaDB(fileMeta, username.(string))

	if !uploadDB {
		c.JSON(200, gin.H{
			"code": 1,
			"msg":  "Internal Server Error: Failed to save metadata to the DB.",
		})
		return
	}

	uploadAWS, err := aws.UploadToAWS(basepath, fileMeta.FileMD5, fileMeta.FileName)
	if !uploadAWS {
		c.JSON(200, gin.H{
			"code":  1,
			"msg":   "Internal Server Error: Failed to save file to the AWS.",
			"error": err.Error(),
		})
		return
	}
	timer.Elapse = time.Since(timer.StartTime)
	fmt.Println("ELAPSE TIME: ", timer.Elapse)

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"msg":  "File successfully uploaded!",
		"data": struct {
			FileMeta *meta.FileMeta `json:"meta"`
		}{
			FileMeta: &fileMeta,
		},
	})
	return
}

// GetFileMetaHandler gets the meta data of the given file from request.form
func GetFileMetaHandler(c *gin.Context) {
	var filehash string
	if err := c.ShouldBindJSON(&filehash); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code": 1,
			"msg":  err.Error(),
		})
		panic(err)
	}

	filemeta, err := meta.GetFileMetaDB(filehash)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":  1,
			"msg":   "Internal Server Error: Failed to retrieve file meta.",
			"error": err.Error(),
		})
		return
	}

	data, err := json.Marshal(filemeta)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":  1,
			"msg":   "Internal Server Error: Failed to retrieve file meta.",
			"error": err.Error(),
		})
		return
	}
	c.Writer.Write(data)
}

// QueryByBatchHandler : query the last `n` files' info. Query file meta by batch.
func QueryByBatchHandler(c *gin.Context) {
	var lim string
	if err := c.ShouldBindJSON(&lim); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code": 1,
			"msg":  err.Error(),
		})
		panic(err)
	}

	// "limit": how many files the user want to query
	count, _ := strconv.Atoi(lim)
	fMetas := meta.GetLastFileMetas(count)

	// return to client as a JSON
	data, err := json.Marshal(fMetas)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":  1,
			"msg":   "Internal Server Error: Failed to query file information.",
			"error": err.Error(),
		})
		return
	}
	c.Writer.Write(data)
}

// DownloadHandler : download file
func DownloadHandler(c *gin.Context) {
	filehash := c.Query("filehash")
	metaInfo, err := meta.GetFileMetaDB(filehash)
	if err != nil {
		panic(err.Error())
	}

	f, err := os.Open(metaInfo.Location)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":     1,
			"msg":      "Internal Server Error: Failed to open file for download.",
			"error":    err.Error(),
			"location": metaInfo.Location,
		})
		return
	}
	defer f.Close()

	// read file into RAM. Assuming the file size is not large
	data, err := ioutil.ReadAll(f)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":  1,
			"msg":   "Internal Server Error: Failed to read file for download.",
			"error": err.Error(),
		})
		return
	}

	c.Writer.Header().Set("Content-Type", "appllication/octect-stream")
	c.Writer.Header().Set("Content-Disposition", "attatchment; filename=\""+metaInfo.FileName+"\"")
	c.Writer.Write(data)
}

// FileUpdateHandler : renames file
func FileUpdateHandler(c *gin.Context) {
	// only accept post request
	if c.Request.Method != "POST" {
		c.JSON(http.StatusMethodNotAllowed, gin.H{
			"code": 1,
			"msg":  "Status Method Not Allowed: Failed to update file - POST request only.",
		})
		return
	}
	c.Request.ParseForm()
	operationType := c.Request.Form.Get("op") // for future use: expand operation type to not only renaming file
	filehash := c.Request.Form.Get("filehash")
	newFileName := c.Request.Form.Get("filename")

	if operationType != "update-name" {
		c.JSON(http.StatusForbidden, gin.H{
			"code": 1,
			"msg":  "Status Forbidden: Failed to update file name.",
		})
		return
	}

	currFileMeta := meta.GetFileMeta(filehash)
	currFileMeta.FileName = newFileName
	meta.UpdateFileMeta(currFileMeta)

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"msg":  "File successfully updated!",
		"data": struct {
			FileMeta *meta.FileMeta `json:"meta"`
		}{
			FileMeta: &currFileMeta,
		},
	})
	return
}

// FileDeleteHandler : delete the file (soft-delete by using a flag)
func FileDeleteHandler(c *gin.Context) {
	var fileHash string = c.Query("filehash")
	var fileName string = c.Query("filename")
	fileMeta, err := meta.GetFileMetaDB(fileHash)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":  1,
			"msg":   "Internal server error: Failed to delete file from the database.",
			"error": err.Error(),
		})
		return
	}

	// getting username
	username, _ := c.Get("username")

	removeFromDB, delFile := meta.RemoveMetaDB(username.(string), fileHash, fileName)
	if !removeFromDB {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code": 1,
			"msg":  "Internal server error: Failed to delete file from the databases.",
		})
		return
	}
	if delFile {
		aws.DeleteFromAWS(fileHash)
	}
	os.Remove(fileMeta.Location)
	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"msg":  "File successfully deleted!",
	})
	return
}

// InstantUpload : check if the file is already in the database by comparing the hash.
// If so, then instant upload is triggered
func InstantUpload(c *gin.Context) {
	timer.StartTime = time.Now()
	fmt.Println("STARTING TIME: ", timer.StartTime)
	fileHash := c.Query("filehash")
	fileHash = strings.TrimRight(fileHash, "\n")
	if fileHash == "" {
		c.JSON(200, gin.H{
			"code": 1,
			"msg":  "Empty filehash received, please wait until the file finish preprocess",
		})
		return
	}

	// //first check if user already has file in server
	username, exist := c.Get("username")
	if !exist {
		fmt.Printf("Failed to find username.")
	}

	// 1. check if file is already in file table
	dup, err := db.IsFileUploaded(fileHash)
	if err != nil {
		panic(err.Error())
	}
	// if the file is already uploaded by anyone before
	if dup {
		// get the filename and the file's meta data in db
		fileName := c.Query("filename")
		fileInfo, err := meta.GetFileMetaDB(fileHash)
		if err != nil {
			panic(err)
		}
		// 2. check if the current user uploaded the same file with the same name before
		duplicateUserFile, err := db.CheckDuplicateUserFile(username.(string), fileHash, fileName)
		if err != nil {
			panic(err.Error())
		}
		// if same filename, filehash in userfile table
		if duplicateUserFile {
			fmt.Print("same file detected uploaded by user")
			timer.Elapse = time.Since(timer.StartTime)
			fmt.Println("DUPLICATE user ELAPSE TIME: ", timer.Elapse)
			c.JSON(200, gin.H{
				"code": 0,
				"msg":  "Duplicate file detected",
				"data": gin.H{
					"shouldUpload": false,
				},
			})
			return
		}
		// update the value `copies` in the database tbl_file table
		err = db.UpdateCopies(fileHash)
		if err != nil {
			panic(err.Error())
		}

		// insert new tuple in tbl_userfile
		_, err = db.OnFileUploadUser(username.(string), fileHash, fileInfo.FileSize, fileName)
		if err != nil {
			panic(err.Error())
		}
		timer.Elapse = time.Since(timer.StartTime)
		fmt.Println("DUPLICATE file ELAPSE TIME: ", timer.Elapse)
		// update successfully
		c.JSON(200, gin.H{
			"code": 0,
			"msg":  "Duplicate file detected",
			"data": gin.H{
				"shouldUpload": false,
			},
		})
		return
	}

	// no duplicated file detected
	c.JSON(200, gin.H{
		"code": 0,
		"msg":  "No dup file detected",
		"data": gin.H{
			"shouldUpload": true,
		},
	})
}

// GetDownloadURL : get the file download url
func GetDownloadURL(c *gin.Context) {
	filehash := c.Query("filehash")
	filename := c.Query("filename")
	metaInfo, err := meta.GetFileMetaDB(filehash)
	if err != nil {
		panic(err.Error())
	}

	if metaInfo.Location == "aws" {
		signedURL := aws.GetDownloadURL(filehash, filename)
		c.Data(200, "octet-stream", []byte(signedURL))
	} else {
		tmpURL := fmt.Sprintf("http://%s/api/file/download?filehash=%s",
			c.Request.Host, filehash)
		c.Data(200, "octet-steam", []byte(tmpURL))
	}
}
